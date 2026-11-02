package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Madian2112/nexo-ai/internal/adapters"
)

func defaultCore(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../internal/source/defaults/soul/core.md")
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFirstInstallInitializesDefaultSource(t *testing.T) {
	home := setup(t)
	r := call("install", "claude")
	if r.code != 0 {
		t.Fatalf("first install: %+v", r)
	}
	core := filepath.Join(home, ".nexo-ai", "source", "soul", "core.md")
	if got := strings.ReplaceAll(readFile(t, core), "\r\n", "\n"); got != defaultCore(t) {
		t.Fatalf("seeded core differs from the embedded soul:\n%s", got)
	}
	if installed := readFile(t, filepath.Join(home, ".claude", "CLAUDE.md")); !strings.Contains(installed, defaultCore(t)) {
		t.Fatalf("CLAUDE.md does not hold the default soul:\n%s", installed)
	}
	if !strings.Contains(r.stdout, "init ~/.nexo-ai/source") || !strings.Contains(r.stdout, "~/.nexo-ai/source/soul/core.md") {
		t.Fatalf("first install does not report the initialization:\n%s", r.stdout)
	}
	// init stays available and has nothing left to do.
	if r := call("init"); r.code != 0 || !strings.Contains(r.stdout, "source already initialized") {
		t.Fatalf("init after first install: %+v", r)
	}
}

func TestFirstInstallDryRunWritesNothing(t *testing.T) {
	home := setup(t)
	r := call("install", "claude", "--dry-run")
	if r.code != 0 {
		t.Fatalf("dry run: %+v", r)
	}
	for _, want := range []string{
		"init ~/.nexo-ai/source (dry run, nothing written)",
		"create    ~/.nexo-ai/source/soul/core.md",
		"claude (dry run, nothing written)",
		"~/.claude/CLAUDE.md",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("dry run output lacks %q:\n%s", want, r.stdout)
		}
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Fatalf("dry run wrote into home: %v", entries)
	}
}

func TestLaterInstallKeepsExistingSource(t *testing.T) {
	home := setup(t)
	if r := call("install", "claude"); r.code != 0 {
		t.Fatalf("first install: %+v", r)
	}
	core := filepath.Join(home, ".nexo-ai", "source", "soul", "core.md")
	writeFile(t, core, "# My custom soul\n")
	r := call("install", "claude")
	if r.code != 0 {
		t.Fatalf("second install: %+v", r)
	}
	if strings.Contains(r.stdout, "init ") {
		t.Errorf("existing source was initialized again:\n%s", r.stdout)
	}
	if got := readFile(t, core); got != "# My custom soul\n" {
		t.Fatalf("custom core overwritten: %q", got)
	}
	if installed := readFile(t, filepath.Join(home, ".claude", "CLAUDE.md")); !strings.Contains(installed, "# My custom soul") {
		t.Fatalf("CLAUDE.md does not hold the custom soul:\n%s", installed)
	}
}

func TestInstallPreservesCustomCoreWithoutPriorInstall(t *testing.T) {
	for _, content := range []string{"# Mine\n", ""} {
		t.Run(content, func(t *testing.T) {
			home := setup(t)
			core := filepath.Join(home, ".nexo-ai", "source", "soul", "core.md")
			writeFile(t, core, content)
			if r := call("install", "claude"); r.code != 0 || strings.Contains(r.stdout, "init ") {
				t.Fatalf("install: %+v", r)
			}
			if got := readFile(t, core); got != content {
				t.Fatalf("core changed: %q", got)
			}
		})
	}
}

func TestInstallRejectsBrokenDefaultSource(t *testing.T) {
	cases := map[string]func(t *testing.T, root string){
		"file instead of directory": func(t *testing.T, root string) { writeFile(t, root, "not a dir") },
		"invalid settings":          func(t *testing.T, root string) { writeFile(t, filepath.Join(root, "nexo.yaml"), "bogus: [") },
		"dangling symlink": func(t *testing.T, root string) {
			if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(filepath.Dir(root), "missing"), root); err != nil {
				t.Skip("symlinks unavailable:", err)
			}
		},
	}
	for name, prepare := range cases {
		t.Run(name, func(t *testing.T) {
			home := setup(t)
			root := filepath.Join(home, ".nexo-ai", "source")
			prepare(t, root)
			for _, args := range [][]string{{"install", "claude", "--dry-run"}, {"install", "claude"}} {
				if r := call(args...); r.code != 1 || !strings.Contains(r.stderr, "error:") {
					t.Fatalf("%v: %+v", args, r)
				}
			}
			if _, err := os.Stat(filepath.Join(root, "soul", "core.md")); err == nil {
				t.Error("broken source was reinitialized")
			}
			if _, err := os.Lstat(filepath.Join(home, ".claude")); !os.IsNotExist(err) {
				t.Error("agent installed from a broken source")
			}
		})
	}
}

func TestInstallNeverInitializesExplicitSource(t *testing.T) {
	home := setup(t)
	custom := filepath.Join(home, "my-source")
	for _, args := range [][]string{{"install", "claude", "--source", custom, "--dry-run"}, {"install", "claude", "--source", custom}} {
		r := call(args...)
		if r.code != 1 || !strings.Contains(r.stderr, "source directory") {
			t.Fatalf("%v: %+v", args, r)
		}
	}
	for _, p := range []string{custom, filepath.Join(home, ".nexo-ai"), filepath.Join(home, ".claude")} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s was created", p)
		}
	}
	// An explicit source that exists keeps working.
	if r := call("install", "claude", "--source", example(t)); r.code != 0 {
		t.Fatalf("explicit source: %+v", r)
	}
}

func TestFirstInstallPlanningErrorWritesNothing(t *testing.T) {
	home := setup(t)
	// kiro is planned after claude; its unreadable state must abort before any write.
	writeFile(t, filepath.Join(home, ".nexo-ai", "state", "kiro.json"), "{oops")
	r := call("install", "all")
	if r.code != 1 || !strings.Contains(r.stderr, "error: kiro:") {
		t.Fatalf("%+v", r)
	}
	for _, p := range []string{filepath.Join(home, ".nexo-ai", "source"), filepath.Join(home, ".claude")} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s written although planning failed", p)
		}
	}
}

func TestFirstInstallApplyErrorLeavesUsableSource(t *testing.T) {
	home := setup(t)
	// A file where the .claude directory belongs makes writing CLAUDE.md fail.
	blocker := filepath.Join(home, ".claude")
	writeFile(t, blocker, "in the way")
	if r := call("install", "claude"); r.code != 1 || !strings.Contains(r.stderr, "error: claude:") {
		t.Fatalf("install over blocker: %+v", r)
	}
	core := filepath.Join(home, ".nexo-ai", "source", "soul", "core.md")
	if got := strings.ReplaceAll(readFile(t, core), "\r\n", "\n"); got != defaultCore(t) {
		t.Fatalf("source left partial: %q", got)
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	r := call("install", "claude")
	if r.code != 0 || strings.Contains(r.stdout, "init ") {
		t.Fatalf("retry: %+v", r)
	}
	readFile(t, filepath.Join(home, ".claude", "CLAUDE.md"))
}

func TestFirstInstallEveryAgent(t *testing.T) {
	for _, target := range append(adapters.Names(), "all") {
		t.Run(target, func(t *testing.T) {
			home := setup(t)
			dry := call("install", target, "--dry-run")
			if dry.code != 0 {
				t.Fatalf("dry run: %+v", dry)
			}
			if entries, _ := os.ReadDir(home); len(entries) != 0 {
				t.Fatalf("dry run wrote into home: %v", entries)
			}
			r := call("install", target)
			if r.code != 0 {
				t.Fatalf("install: %+v", r)
			}
			readFile(t, filepath.Join(home, ".nexo-ai", "source", "soul", "core.md"))
			names := []string{target}
			if target == "all" {
				names = adapters.Names()
			}
			for _, n := range names {
				readFile(t, filepath.Join(home, ".nexo-ai", "state", n+".json"))
				if !strings.Contains(r.stdout, "\n"+n+"\n") {
					t.Errorf("%s not reported:\n%s", n, r.stdout)
				}
			}
		})
	}
}
