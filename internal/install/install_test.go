package install

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Madian2112/nexo-ai/internal/plan"
)

type fixture struct {
	home string
	opts Options
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	home := t.TempDir()
	return fixture{home: home, opts: Options{
		Home:      home,
		StateDir:  filepath.Join(home, ".nexo-ai", "state"),
		BackupDir: filepath.Join(home, ".nexo-ai", "backups", "run1"),
	}}
}

func (f fixture) path(rel string) string { return filepath.Join(f.home, filepath.FromSlash(rel)) }

func actions(r Report) map[string]string {
	out := map[string]string{}
	for _, e := range r.Entries {
		out[e.Path] = e.Action
	}
	return out
}

func mustApply(t *testing.T, p plan.Plan, opts Options) Report {
	t.Helper()
	r, err := Apply(p, opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return r
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func skillDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "tdd")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("skill"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestCreateThenIdempotent(t *testing.T) {
	f := newFixture(t)
	skill := skillDir(t)
	p := plan.Plan{Agent: "claude"}
	p.File(f.path(".claude/CLAUDE.md"), []byte("soul\n"))
	p.Link(f.path(".claude/skills/tdd"), skill)

	r := mustApply(t, p, f.opts)
	got := actions(r)
	if got[f.path(".claude/CLAUDE.md")] != ActionCreate || got[f.path(".claude/skills/tdd")] != ActionLink {
		t.Fatalf("first run: %v", got)
	}
	if read(t, f.path(".claude/skills/tdd/SKILL.md")) != "skill" {
		t.Error("link does not resolve to the skill")
	}

	r = mustApply(t, p, f.opts)
	for path, a := range actions(r) {
		if a != ActionUnchanged {
			t.Errorf("second run %s = %s, want unchanged", path, a)
		}
	}
	if _, err := os.Stat(f.opts.BackupDir); !os.IsNotExist(err) {
		t.Error("nothing was overwritten, so no backup dir should exist")
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	f := newFixture(t)
	p := plan.Plan{Agent: "claude"}
	p.File(f.path(".claude/CLAUDE.md"), []byte("soul\n"))
	p.Link(f.path(".claude/skills/tdd"), skillDir(t))
	opts := f.opts
	opts.DryRun = true

	r := mustApply(t, p, opts)
	if actions(r)[f.path(".claude/CLAUDE.md")] != ActionCreate {
		t.Errorf("dry run should report the planned action: %v", actions(r))
	}
	entries, _ := os.ReadDir(f.home)
	if len(entries) != 0 {
		t.Fatalf("dry run wrote %d entries into home", len(entries))
	}
}

func TestBackupUserFileBeforeOverwrite(t *testing.T) {
	f := newFixture(t)
	target := f.path(".claude/CLAUDE.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("hand written\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := plan.Plan{Agent: "claude"}
	p.File(target, []byte("soul\n"))

	r := mustApply(t, p, f.opts)
	if r.Entries[0].Action != ActionUpdate || !r.Entries[0].BackedUp {
		t.Fatalf("entry: %+v", r.Entries[0])
	}
	if read(t, target) != "soul\n" {
		t.Error("file not written")
	}
	if read(t, filepath.Join(f.opts.BackupDir, ".claude", "CLAUDE.md")) != "hand written\n" {
		t.Error("original not backed up")
	}

	// Once nexo-ai owns it, later updates do not pile up backups.
	p2 := plan.Plan{Agent: "claude"}
	p2.File(target, []byte("soul v2\n"))
	opts := f.opts
	opts.BackupDir = filepath.Join(f.home, ".nexo-ai", "backups", "run2")
	r = mustApply(t, p2, opts)
	if r.Entries[0].BackedUp {
		t.Error("an owned file should be updated without a new backup")
	}
}

func TestExistingDirectoryAtSkillPathIsBackedUp(t *testing.T) {
	f := newFixture(t)
	target := f.path(".claude/skills/tdd")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "SKILL.md"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := plan.Plan{Agent: "claude"}
	p.Link(target, skillDir(t))

	r := mustApply(t, p, f.opts)
	if !r.Entries[0].BackedUp || read(t, filepath.Join(target, "SKILL.md")) != "skill" {
		t.Fatalf("entry %+v", r.Entries[0])
	}
	if read(t, filepath.Join(f.opts.BackupDir, ".claude", "skills", "tdd", "SKILL.md")) != "old" {
		t.Error("old skill dir not backed up")
	}
}

func TestStaleOwnedEntriesAreRemovedButMergedFilesStay(t *testing.T) {
	f := newFixture(t)
	p := plan.Plan{Agent: "claude"}
	p.File(f.path(".claude/rules/nexo-old.md"), []byte("old rule\n"))
	p.Link(f.path(".claude/skills/tdd"), skillDir(t))
	p.Merged(f.path(".claude/settings.json"), []byte("{}\n"), []string{"hooks"})
	mustApply(t, p, f.opts)
	keys, err := OwnedKeys(f.opts.StateDir, "claude")
	if err != nil || len(keys[f.path(".claude/settings.json")]) != 1 {
		t.Fatalf("shared keys must be recorded in state: %v %v", keys, err)
	}

	r := mustApply(t, plan.Plan{Agent: "claude"}, f.opts)
	got := actions(r)
	if got[f.path(".claude/rules/nexo-old.md")] != ActionRemove || got[f.path(".claude/skills/tdd")] != ActionRemove {
		t.Fatalf("stale entries not removed: %v", got)
	}
	if _, err := os.Lstat(f.path(".claude/rules/nexo-old.md")); !os.IsNotExist(err) {
		t.Error("stale rule still on disk")
	}
	if _, err := os.Stat(f.path(".claude/settings.json")); err != nil {
		t.Error("merged shared file must never be removed")
	}
}

func TestCopyLinks(t *testing.T) {
	f := newFixture(t)
	skill := skillDir(t)
	p := plan.Plan{Agent: "kiro"}
	p.Link(f.path(".kiro/skills/tdd"), skill)
	opts := f.opts
	opts.CopyLinks = true

	r := mustApply(t, p, opts)
	info, err := os.Lstat(f.path(".kiro/skills/tdd"))
	if err != nil || info.Mode()&os.ModeSymlink != 0 || r.Entries[0].Action != ActionCopy {
		t.Fatalf("want a real copied dir, got %v %v", info, r.Entries)
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	r = mustApply(t, p, opts)
	if r.Entries[0].BackedUp || read(t, f.path(".kiro/skills/tdd/SKILL.md")) != "v2" {
		t.Fatalf("recopy of an owned dir: %+v", r.Entries[0])
	}
	r = mustApply(t, p, opts)
	if r.Entries[0].Action != ActionUnchanged {
		t.Fatalf("an identical copy should be unchanged: %+v", r.Entries[0])
	}
}

func TestFileModeIsPreserved(t *testing.T) {
	f := newFixture(t)
	path := f.path(".claude.json")
	if err := os.WriteFile(path, []byte(`{"oauthAccount": "secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	p := plan.Plan{Agent: "claude"}
	p.Merged(path, []byte(`{"oauthAccount": "secret", "mcpServers": {}}`), []string{"mcpServers"})
	mustApply(t, p, f.opts)
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600 (err %v)", info.Mode().Perm(), err)
	}
}

// userReplacesLink installs a skill link, then replaces it with a real directory.
func userReplacesLink(t *testing.T, f fixture) plan.Plan {
	t.Helper()
	p := plan.Plan{Agent: "claude"}
	p.Link(f.path(".claude/skills/third"), skillDir(t))
	mustApply(t, p, f.opts)
	target := f.path(".claude/skills/third")
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "SKILL.md"), []byte("precious"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestUserChangedStaleEntryIsBackedUp(t *testing.T) {
	f := newFixture(t)
	userReplacesLink(t, f)

	r := mustApply(t, plan.Plan{Agent: "claude"}, f.opts)
	if len(r.Entries) != 1 || r.Entries[0].Action != ActionRemove || !r.Entries[0].BackedUp {
		t.Fatalf("entries: %+v", r.Entries)
	}
	if read(t, filepath.Join(f.opts.BackupDir, ".claude", "skills", "third", "SKILL.md")) != "precious" {
		t.Error("user content must land in the backup")
	}
}

func TestUserChangedOwnedEntryIsBackedUpOnRelink(t *testing.T) {
	f := newFixture(t)
	p := userReplacesLink(t, f)

	r := mustApply(t, p, f.opts)
	if r.Entries[0].Action != ActionLink || !r.Entries[0].BackedUp {
		t.Fatalf("entry: %+v", r.Entries[0])
	}
	if read(t, filepath.Join(f.opts.BackupDir, ".claude", "skills", "third", "SKILL.md")) != "precious" {
		t.Error("user content must land in the backup")
	}
}

func TestUserEditedOwnedFileIsBackedUp(t *testing.T) {
	f := newFixture(t)
	path := f.path(".claude/CLAUDE.md")
	p := plan.Plan{Agent: "claude"}
	p.File(path, []byte("soul\n"))
	mustApply(t, p, f.opts)
	if err := os.WriteFile(path, []byte("soul + my notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p2 := plan.Plan{Agent: "claude"}
	p2.File(path, []byte("soul v2\n"))
	r := mustApply(t, p2, f.opts)
	if !r.Entries[0].BackedUp || read(t, filepath.Join(f.opts.BackupDir, ".claude", "CLAUDE.md")) != "soul + my notes\n" {
		t.Fatalf("hand edits of an owned file must be backed up: %+v", r.Entries[0])
	}
}

func TestDuplicatePathsAreRejected(t *testing.T) {
	f := newFixture(t)
	p := plan.Plan{Agent: "kiro"}
	p.File(f.path(".kiro/steering/nexo-core.md"), []byte("a"))
	p.File(f.path(".kiro/steering/nexo-core.md"), []byte("b"))
	if _, err := Apply(p, f.opts); err == nil {
		t.Fatal("want an error for two ops on one path")
	}
	if _, err := os.Stat(f.path(".kiro")); !os.IsNotExist(err) {
		t.Error("nothing may be written for an invalid plan")
	}
}

func TestNewSharedFileIsPrivate(t *testing.T) {
	f := newFixture(t)
	p := plan.Plan{Agent: "claude"}
	p.Merged(f.path(".claude.json"), []byte("{}\n"), []string{"mcpServers"})
	p.File(f.path(".claude/CLAUDE.md"), []byte("soul\n"))
	mustApply(t, p, f.opts)
	if info, _ := os.Stat(f.path(".claude.json")); info.Mode().Perm() != 0o600 {
		t.Errorf("new shared file mode = %v, want 0600", info.Mode().Perm())
	}
	if info, _ := os.Stat(f.path(".claude/CLAUDE.md")); info.Mode().Perm() != 0o644 {
		t.Errorf("owned file mode = %v, want 0644", info.Mode().Perm())
	}
}

func TestStatelessApplyKeepsNoState(t *testing.T) {
	f := newFixture(t)
	opts := f.opts
	opts.StateDir = ""
	p := plan.Plan{Agent: "import"}
	p.File(f.path("src/soul/core.md"), []byte("soul\n"))
	mustApply(t, p, opts)
	r := mustApply(t, plan.Plan{Agent: "import"}, opts)
	if len(r.Entries) != 0 {
		t.Fatalf("without state nothing is stale: %+v", r.Entries)
	}
	if _, err := os.Stat(f.path("src/soul/core.md")); err != nil {
		t.Error("file must stay")
	}
	if _, err := os.Stat(f.path(".nexo-ai")); !os.IsNotExist(err) {
		t.Error("no state dir may be created")
	}
}
