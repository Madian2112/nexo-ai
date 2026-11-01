// Package install applies a plan to disk: idempotent writes, backups before any
// overwrite, and removal of entries nexo-ai owned that left the source.
//
// Every entry nexo-ai owns is recorded with a fingerprint of what it wrote. An owned
// entry is replaced or removed without a backup only while it still matches that
// fingerprint; anything the user changed is moved to the backup directory first.
package install

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Madian2112/nexo-ai/internal/plan"
)

const (
	ActionCreate    = "create"
	ActionUpdate    = "update"
	ActionUnchanged = "unchanged"
	ActionLink      = "link"
	ActionCopy      = "copy"
	ActionRemove    = "remove"
)

type Options struct {
	Home      string
	StateDir  string
	BackupDir string
	DryRun    bool
	// CopyLinks copies skill directories instead of symlinking them.
	CopyLinks bool
}

type Entry struct {
	Path     string
	Action   string
	BackedUp bool
}

type Report struct {
	Agent    string
	Entries  []Entry
	Warnings []string
}

// state maps each owned entry to its fingerprint, and each shared file to the keys
// nexo-ai wrote into it.
type state struct {
	Owned  map[string]string   `json:"owned"`
	Shared map[string][]string `json:"shared"`
}

func statePath(dir, agent string) string { return filepath.Join(dir, agent+".json") }

// OwnedKeys returns the keys a previous install wrote into each shared file, for plan.Env.
func OwnedKeys(stateDir, agent string) (map[string][]string, error) {
	s, err := readState(statePath(stateDir, agent))
	return s.Shared, err
}

func Apply(p plan.Plan, opts Options) (Report, error) {
	r := Report{Agent: p.Agent, Warnings: p.Warnings}
	if err := p.Validate(); err != nil {
		return r, err
	}
	// Without a state dir (import into the source) nothing is owned: no stale removal, no state.
	stateful := opts.StateDir != ""
	sp := statePath(opts.StateDir, p.Agent)
	prev := state{Owned: map[string]string{}, Shared: map[string][]string{}}
	var err error
	if stateful {
		if prev, err = readState(sp); err != nil {
			return r, err
		}
	}
	a := applier{opts: opts, prev: prev}

	next := state{Owned: map[string]string{}, Shared: map[string][]string{}}
	planned := map[string]bool{}
	for _, op := range p.Ops {
		planned[op.Path] = true
		var e Entry
		var fp string
		if op.IsLink() {
			e, fp, err = a.link(op)
		} else {
			e, err = a.file(op)
			fp = fileFingerprint(op.Content)
		}
		if err != nil {
			return r, fmt.Errorf("%s: %w", op.Path, err)
		}
		if op.Owned {
			next.Owned[op.Path] = fp
		} else if len(op.Keys) > 0 {
			next.Shared[op.Path] = op.Keys
		}
		r.Entries = append(r.Entries, e)
	}

	stale := make([]string, 0, len(prev.Owned))
	for path := range prev.Owned {
		if !planned[path] {
			stale = append(stale, path)
		}
	}
	sort.Strings(stale)
	for _, path := range stale {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		}
		e := Entry{Path: path, Action: ActionRemove, BackedUp: !a.untouched(path)}
		if !opts.DryRun {
			if err := a.clear(path, e.BackedUp); err != nil {
				return r, err
			}
		}
		r.Entries = append(r.Entries, e)
	}

	if stateful && !opts.DryRun {
		if err := writeFile(sp, plan.JSON(next)); err != nil {
			return r, err
		}
	}
	return r, nil
}

type applier struct {
	opts Options
	prev state
}

// untouched reports whether path still holds exactly what nexo-ai wrote last time.
func (a applier) untouched(path string) bool {
	want, ok := a.prev.Owned[path]
	if !ok {
		return false
	}
	got, err := fingerprint(path)
	return err == nil && got == want
}

// clear moves path to the backup directory, or deletes it when backup is false.
func (a applier) clear(path string, backup bool) error {
	if backup {
		return a.backup(path)
	}
	return os.RemoveAll(path)
}

func (a applier) file(op plan.Op) (Entry, error) {
	e := Entry{Path: op.Path, Action: ActionCreate}
	info, err := os.Lstat(op.Path)
	mode := fs.FileMode(0o644)
	if !op.Owned {
		// Shared config files (.claude.json, mcp.json, config.toml) can hold tokens.
		mode = 0o600
	}
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return e, err
	case info.Mode().IsRegular():
		current, err := os.ReadFile(op.Path)
		if err != nil {
			return e, err
		}
		if fileFingerprint(current) == fileFingerprint(op.Content) {
			e.Action = ActionUnchanged
			return e, nil
		}
		e.Action = ActionUpdate
		e.BackedUp = !a.untouched(op.Path)
		mode = info.Mode().Perm()
	default:
		// A directory or symlink sits where a file belongs: move it aside.
		e.Action = ActionUpdate
		e.BackedUp = true
	}
	if a.opts.DryRun {
		return e, nil
	}
	if e.BackedUp {
		if err := a.backup(op.Path); err != nil {
			return e, err
		}
	}
	return e, writeFileMode(op.Path, op.Content, mode)
}

func (a applier) link(op plan.Op) (Entry, string, error) {
	e := Entry{Path: op.Path, Action: ActionLink}
	want := "link:" + op.LinkTo
	if a.opts.CopyLinks {
		e.Action = ActionCopy
		tree, err := treeHash(op.LinkTo)
		if err != nil {
			return e, "", err
		}
		want = "copy:" + tree
	}
	_, err := os.Lstat(op.Path)
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return e, want, err
	}
	if exists {
		if got, err := fingerprint(op.Path); err == nil && got == want {
			e.Action = ActionUnchanged
			return e, want, nil
		}
	}
	e.BackedUp = exists && !a.untouched(op.Path)
	if a.opts.DryRun {
		return e, want, nil
	}
	if exists {
		if err := a.clear(op.Path, e.BackedUp); err != nil {
			return e, want, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(op.Path), 0o755); err != nil {
		return e, want, err
	}
	if !a.opts.CopyLinks {
		if err := os.Symlink(op.LinkTo, op.Path); err == nil {
			return e, want, nil
		}
		// Windows without Developer Mode cannot create symlinks: fall back to a copy.
		e.Action = ActionCopy
	}
	if err := copyTree(op.LinkTo, op.Path); err != nil {
		return e, want, err
	}
	tree, err := treeHash(op.Path)
	return e, "copy:" + tree, err
}

func (a applier) backup(path string) error {
	rel, err := filepath.Rel(a.opts.Home, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		rel = strings.TrimLeft(filepath.ToSlash(path), "/:")
	}
	dest := filepath.Join(a.opts.BackupDir, rel)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if err := os.Rename(path, dest); err == nil {
		return nil
	}
	if err := copyTree(path, dest); err != nil {
		return err
	}
	return os.RemoveAll(path)
}

// fingerprint identifies what is on disk at path without following a symlink.
func fingerprint(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(path)
		return "link:" + target, err
	case info.IsDir():
		tree, err := treeHash(path)
		return "copy:" + tree, err
	default:
		b, err := os.ReadFile(path)
		return fileFingerprint(b), err
	}
}

func fileFingerprint(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func treeHash(root string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%s\x00", filepath.ToSlash(rel), d.Type())
		if d.Type().IsRegular() {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			if _, err := io.Copy(h, f); err != nil {
				return err
			}
		}
		return nil
	})
	return hex.EncodeToString(h.Sum(nil)), err
}

func readState(path string) (state, error) {
	s := state{Owned: map[string]string{}, Shared: map[string][]string{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("state file %s is corrupt: %w", path, err)
	}
	return s, nil
}

func writeFile(path string, content []byte) error {
	return writeFileMode(path, content, 0o644)
}

// writeFileMode writes atomically and keeps mode, so a 0600 file holding tokens stays 0600.
func writeFileMode(path string, content []byte, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".nexo-tmp"
	if err := os.WriteFile(tmp, content, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if d.Type()&fs.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.Create(target)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}
