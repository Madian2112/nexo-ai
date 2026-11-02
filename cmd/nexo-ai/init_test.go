package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitCleanInstallation(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n"} {
		t.Run(map[string]string{"\n": "LF", "\r\n": "CRLF"}[newline], func(t *testing.T) {
			home := setup(t)
			if r := call("measure", "claude"); r.code != 1 {
				t.Fatalf("measure without source: %+v", r)
			}
			if r := call("import", "claude"); r.code != 0 || !strings.Contains(r.stdout, "nothing new to import") {
				t.Fatalf("empty import: %+v", r)
			}
			root := filepath.Join(home, ".nexo-ai", "source")
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatalf("empty import created source: %v", err)
			}
			if r := call("init"); r.code != 0 {
				t.Fatalf("init: %+v", r)
			}
			core, err := os.ReadFile(filepath.Join(root, "soul", "core.md"))
			if err != nil || len(core) == 0 {
				t.Fatalf("default core: %q, %v", core, err)
			}
			// Simulate source checkout line endings on every operating system.
			core = []byte(strings.ReplaceAll(strings.ReplaceAll(string(core), "\r\n", "\n"), "\n", newline))
			if err := os.WriteFile(filepath.Join(root, "soul", "core.md"), core, 0644); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(home, ".claude")); !os.IsNotExist(err) {
				t.Fatalf("init installed agent: %v", err)
			}
			if r := call("install", "claude"); r.code != 0 {
				t.Fatalf("install after init: %+v", r)
			}
			installed, err := os.ReadFile(filepath.Join(home, ".claude", "CLAUDE.md"))
			// The source loader canonicalizes CRLF to LF before compilation.
			expected := strings.ReplaceAll(string(core), "\r\n", "\n")
			if err != nil || !strings.Contains(string(installed), expected) {
				t.Fatalf("installed core: %q, %v", installed, err)
			}
		})
	}
}

func TestInitPreservesExistingCore(t *testing.T) {
	for _, content := range []string{"# My custom soul\n", ""} {
		t.Run(content, func(t *testing.T) {
			home := setup(t)
			root := filepath.Join(home, ".nexo-ai", "source")
			core := filepath.Join(root, "soul", "core.md")
			if err := os.MkdirAll(filepath.Dir(core), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(core, []byte(content), 0644); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if r := call("init"); r.code != 0 {
					t.Fatalf("init: %+v", r)
				}
			}
			got, err := os.ReadFile(core)
			if err != nil || string(got) != content {
				t.Fatalf("core overwritten: %q, %v", got, err)
			}
			entries, err := os.ReadDir(filepath.Join(home, ".nexo-ai"))
			if err != nil || len(entries) != 1 {
				t.Fatalf("unexpected state/backups: %v, %v", entries, err)
			}
		})
	}
}

func TestInitPathsAndDryRun(t *testing.T) {
	for _, custom := range []bool{false, true} {
		t.Run(map[bool]string{false: "NEXO_HOME", true: "source flag"}[custom], func(t *testing.T) {
			home := setup(t)
			nexo := filepath.Join(home, "custom-home")
			t.Setenv("NEXO_HOME", nexo)
			root := filepath.Join(nexo, "source")
			args := []string{"init"}
			if custom {
				root = filepath.Join(home, "custom-source")
				args = append(args, "--source", root)
			}
			r := call(append(args, "--dry-run")...)
			if r.code != 0 || !strings.Contains(r.stdout, "nothing written") {
				t.Fatalf("dry run: %+v", r)
			}
			entries, _ := os.ReadDir(home)
			if len(entries) != 0 {
				t.Fatalf("dry run wrote: %v", entries)
			}
			if r := call(args...); r.code != 0 {
				t.Fatalf("init: %+v", r)
			}
			if _, err := os.Stat(filepath.Join(root, "soul", "core.md")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestInitRejectsArguments(t *testing.T) {
	home := setup(t)
	for _, args := range [][]string{{"init", "claude"}, {"init", "--bogus"}, {"init", "--source"}} {
		if r := call(args...); r.code != 2 {
			t.Errorf("%v: %+v", args, r)
		}
	}
	entries, _ := os.ReadDir(home)
	if len(entries) != 0 {
		t.Fatalf("invalid args wrote: %v", entries)
	}
}

func TestInitRejectsConflictingSource(t *testing.T) {
	for _, kind := range []string{"root file", "soul file", "core directory", "invalid config", "soul symlink", "core symlink"} {
		t.Run(kind, func(t *testing.T) {
			home := setup(t)
			root := filepath.Join(home, ".nexo-ai", "source")
			if err := os.MkdirAll(filepath.Dir(root), 0755); err != nil {
				t.Fatal(err)
			}
			if kind == "root file" {
				if err := os.WriteFile(root, []byte("keep"), 0644); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.MkdirAll(root, 0755); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "soul file":
					if err := os.WriteFile(filepath.Join(root, "soul"), []byte("keep"), 0644); err != nil {
						t.Fatal(err)
					}
				case "core directory":
					if err := os.MkdirAll(filepath.Join(root, "soul", "core.md"), 0755); err != nil {
						t.Fatal(err)
					}
				case "invalid config":
					if err := os.WriteFile(filepath.Join(root, "nexo.yaml"), []byte("[invalid"), 0644); err != nil {
						t.Fatal(err)
					}
				default:
					outside := t.TempDir()
					link := filepath.Join(root, "soul")
					if kind == "core symlink" {
						if err := os.MkdirAll(link, 0755); err != nil {
							t.Fatal(err)
						}
						link = filepath.Join(link, "core.md")
					}
					if err := os.Symlink(outside, link); err != nil {
						t.Skipf("symlinks unavailable: %v", err)
					}
				}
			}
			if r := call("init"); r.code != 1 {
				t.Fatalf("conflict accepted: %+v", r)
			}
			if kind == "root file" {
				b, _ := os.ReadFile(root)
				if string(b) != "keep" {
					t.Fatal("root overwritten")
				}
			}
			if kind == "invalid config" {
				if _, err := os.Stat(filepath.Join(root, "soul")); !os.IsNotExist(err) {
					t.Fatalf("wrote before validation: %v", err)
				}
			}
		})
	}
}

func TestInitNexoSoul(t *testing.T) {
	home := setup(t)
	if r := call("init"); r.code != 0 {
		t.Fatalf("init: %+v", r)
	}
	core, err := os.ReadFile(filepath.Join(home, ".nexo-ai", "source", "soul", "core.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"# Nexo AI — Core Soul", "## Identity", "## Communication", "## Engineering", "## Safety", "## Collaboration", "Never claim to have run a command or test unless it was executed.", "Do not commit, push, merge, deploy, or delete resources without authorization."} {
		if !strings.Contains(string(core), required) {
			t.Errorf("missing %q", required)
		}
	}
}
