package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type result struct {
	code           int
	stdout, stderr string
}

func setup(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	setHome(t, home)
	t.Setenv("NEXO_HOME", filepath.Join(home, ".nexo-ai"))
	t.Setenv("XDG_CONFIG_HOME", "")
	return home
}

func call(args ...string) result {
	var out, errOut bytes.Buffer
	now := func() time.Time { return time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC) }
	code := run(args, &out, &errOut, now)
	return result{code, out.String(), errOut.String()}
}

func example(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs("../../source-example")
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestUsageAndErrors(t *testing.T) {
	setup(t)
	cases := []struct {
		args []string
		code int
		want string
	}{
		{nil, 2, "Usage:"},
		{[]string{"help"}, 0, "nexo-ai install <agent|all>"},
		{[]string{"version"}, 0, "nexo-ai 0.3.0-preview.1"},
		{[]string{"bogus"}, 2, `unknown command "bogus"`},
		{[]string{"install"}, 2, "install needs an agent"},
		{[]string{"install", "cursor"}, 2, `unknown agent "cursor"`},
		{[]string{"install", "claude", "extra"}, 2, "unexpected arguments: extra"},
		{[]string{"measure", "claude"}, 1, "hint: pass --source DIR, or run nexo-ai init to create"},
	}
	for _, c := range cases {
		r := call(c.args...)
		if r.code != c.code || !strings.Contains(r.stdout+r.stderr, c.want) {
			t.Errorf("%v: code=%d want %d, output:\n%s%s", c.args, r.code, c.code, r.stdout, r.stderr)
		}
	}
}

func TestInstallAllEndToEnd(t *testing.T) {
	home := setup(t)
	src := example(t)

	dry := call("install", "all", "--source", src, "--dry-run")
	if dry.code != 0 || !strings.Contains(dry.stdout, "claude (dry run, nothing written)") {
		t.Fatalf("dry run: %d\n%s%s", dry.code, dry.stdout, dry.stderr)
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Fatalf("dry run wrote into home: %v", entries)
	}

	r := call("install", "all", "--source", src)
	if r.code != 0 {
		t.Fatalf("install: %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	for _, rel := range []string{
		".claude/CLAUDE.md", ".claude/rules/nexo-stack-dotnet.md", ".claude/skills/tdd/SKILL.md", ".claude/settings.json", ".claude.json",
		".kiro/steering/nexo-core.md", ".kiro/agents/dotnet-pro.json", ".kiro/hooks/nexo.json", ".kiro/settings/cli.json",
		".codex/AGENTS.md", ".codex/config.toml", ".codex/hooks.json", ".agents/skills/tdd/SKILL.md",
		".nexo-ai/state/claude.json",
	} {
		if _, err := os.Stat(filepath.Join(home, rel)); err != nil {
			t.Errorf("missing %s: %v", rel, err)
		}
	}
	if !strings.Contains(r.stdout, "warning:") {
		t.Error("warnings should be printed")
	}

	again := call("install", "all", "--source", src)
	for _, line := range strings.Split(again.stdout, "\n") {
		if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "  unchanged") && !strings.HasPrefix(line, "  warning:") {
			t.Errorf("second install should be a no-op, got %q", line)
		}
	}
}

func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if info.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestEmptiedCategoriesLeaveSharedFiles(t *testing.T) {
	home := setup(t)
	src := filepath.Join(t.TempDir(), "src")
	copyDir(t, example(t), src)
	if r := call("install", "all", "--source", src); r.code != 0 {
		t.Fatalf("%d\n%s%s", r.code, r.stdout, r.stderr)
	}
	for _, f := range []string{"mcp.yaml", "hooks.yaml", "permissions.yaml", "agents/dotnet-pro.md"} {
		if err := os.Remove(filepath.Join(src, f)); err != nil {
			t.Fatal(err)
		}
	}
	if r := call("install", "all", "--source", src); r.code != 0 {
		t.Fatalf("%d\n%s%s", r.code, r.stdout, r.stderr)
	}
	for rel, gone := range map[string]string{
		".claude.json":            "engram",
		".claude/settings.json":   "guards",
		".kiro/settings/mcp.json": "engram",
		".kiro/settings/cli.json": "toolSearch",
		".codex/config.toml":      "engram",
	} {
		b, err := os.ReadFile(filepath.Join(home, rel))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), gone) {
			t.Errorf("%s still contains %q:\n%s", rel, gone, b)
		}
	}
	for _, rel := range []string{".kiro/hooks/nexo.json", ".codex/hooks.json", ".kiro/settings/permissions.yaml"} {
		if _, err := os.Stat(filepath.Join(home, rel)); !os.IsNotExist(err) {
			t.Errorf("%s should be removed", rel)
		}
	}
}

func TestInstallBacksUpAndReportsDir(t *testing.T) {
	home := setup(t)
	path := filepath.Join(home, ".claude", "CLAUDE.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := call("install", "--source", example(t), "claude")
	if r.code != 0 || !strings.Contains(r.stdout, "update    ~/.claude/CLAUDE.md  (backup)") || !strings.Contains(r.stdout, "backups: ~/.nexo-ai/backups/20261029-120000") {
		t.Fatalf("%d\n%s%s", r.code, r.stdout, r.stderr)
	}
	b, err := os.ReadFile(filepath.Join(home, ".nexo-ai", "backups", "20261029-120000", ".claude", "CLAUDE.md"))
	if err != nil || string(b) != "mine\n" {
		t.Fatalf("backup: %q %v", b, err)
	}
}

func TestInstallAbortsBeforeWritingOnBadConfig(t *testing.T) {
	home := setup(t)
	bad := filepath.Join(home, ".kiro", "settings", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(bad), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte("{oops"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := call("install", "all", "--source", example(t))
	if r.code != 1 || !strings.Contains(r.stderr, "refusing to overwrite") {
		t.Fatalf("%d\n%s%s", r.code, r.stdout, r.stderr)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude")); !os.IsNotExist(err) {
		t.Error("claude was planned before kiro but must not be written when kiro fails")
	}
}

func TestDefaultSourceFromNexoHome(t *testing.T) {
	home := setup(t)
	if err := os.MkdirAll(filepath.Join(home, ".nexo-ai"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(example(t), filepath.Join(home, ".nexo-ai", "source")); err != nil {
		t.Fatal(err)
	}
	if r := call("measure", "claude"); r.code != 0 {
		t.Fatalf("%d\n%s%s", r.code, r.stdout, r.stderr)
	}
}

func TestMeasure(t *testing.T) {
	setup(t)
	r := call("measure", "--source", example(t))
	if r.code != 0 {
		t.Fatalf("%d\n%s", r.code, r.stderr)
	}
	for _, want := range []string{"claude  always ~", "codex  always ~", "kiro  always ~", "not installed", "deferred behind native tool search"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("missing %q in:\n%s", want, r.stdout)
		}
	}

	j := call("measure", "kiro", "--json", "--source", example(t))
	var reports []map[string]any
	if err := json.Unmarshal([]byte(j.stdout), &reports); err != nil || len(reports) != 1 || reports[0]["agent"] != "kiro" {
		t.Fatalf("json: %v\n%s", err, j.stdout)
	}
}

func TestImportThenInstallRoundTrip(t *testing.T) {
	setup(t)
	if r := call("install", "all", "--source", example(t)); r.code != 0 {
		t.Fatalf("%d\n%s%s", r.code, r.stdout, r.stderr)
	}
	src := filepath.Join(t.TempDir(), "imported")

	dry := call("import", "all", "--source", src, "--dry-run")
	if dry.code != 0 || !strings.Contains(dry.stdout, "(dry run, nothing written)") {
		t.Fatalf("%d\n%s%s", dry.code, dry.stdout, dry.stderr)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatal("dry run created the source")
	}

	r := call("import", "all", "--source", src)
	if r.code != 0 {
		t.Fatalf("%d\n%s%s", r.code, r.stdout, r.stderr)
	}
	for _, rel := range []string{"soul/core.md", "soul/sections/stack-dotnet.md", "skills/tdd/SKILL.md", "agents/dotnet-pro.md", "mcp.yaml", "hooks.yaml", "permissions.yaml"} {
		if _, err := os.Stat(filepath.Join(src, rel)); err != nil {
			t.Errorf("missing %s: %v", rel, err)
		}
	}
	if info, err := os.Lstat(filepath.Join(src, "skills", "tdd")); err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Error("skills are copied into the source, never linked to an agent dir")
	}
	if info, _ := os.Stat(filepath.Join(src, "mcp.yaml")); runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("mcp.yaml mode = %v, want 0600", info.Mode().Perm())
	}

	hooks, _ := os.ReadFile(filepath.Join(src, "hooks.yaml"))
	if !strings.Contains(string(hooks), "--harness {{agent}}") || strings.Contains(string(hooks), "--harness claude") {
		t.Errorf("the same hook from three agents must come back as one {{agent}} hook:\n%s", hooks)
	}

	again := call("import", "all", "--source", src)
	if again.code != 0 || !strings.Contains(again.stdout, "nothing new to import") {
		t.Fatalf("second import should add nothing:\n%s%s", again.stdout, again.stderr)
	}

	// The imported source installs back cleanly into a new home.
	setHome(t, t.TempDir())
	if r := call("install", "all", "--source", src); r.code != 0 {
		t.Fatalf("install from imported source: %d\n%s%s", r.code, r.stdout, r.stderr)
	}
}

func TestImportKeepsExistingSource(t *testing.T) {
	setup(t)
	home := os.Getenv("HOME")
	src := filepath.Join(t.TempDir(), "src")
	copyDir(t, example(t), src)
	core := filepath.Join(src, "soul", "core.md")
	before, _ := os.ReadFile(core)
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "AGENTS.md"), []byte("a different soul\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := call("import", "codex", "--source", src)
	if r.code != 0 || !strings.Contains(r.stdout, "soul/core.md already exists") {
		t.Fatalf("%d\n%s%s", r.code, r.stdout, r.stderr)
	}
	after, _ := os.ReadFile(core)
	if string(after) != string(before) {
		t.Error("import must never overwrite the source's core")
	}
}

func TestImportUsage(t *testing.T) {
	setup(t)
	if r := call("import"); r.code != 2 || !strings.Contains(r.stderr, "import needs an agent") {
		t.Errorf("%d %s", r.code, r.stderr)
	}
	if r := call("import", "cursor"); r.code != 2 || !strings.Contains(r.stderr, `unknown agent "cursor"`) {
		t.Errorf("%d %s", r.code, r.stderr)
	}
}

func TestImportNothingIntoMissingSource(t *testing.T) {
	setup(t)
	src := filepath.Join(t.TempDir(), "never")
	r := call("import", "codex", "--source", src)
	if r.code != 0 || !strings.Contains(r.stdout, "nothing new to import") {
		t.Fatalf("%d\n%s%s", r.code, r.stdout, r.stderr)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("an empty import must not create the source")
	}
}

// Same guard in Claude and Kiro, written with different home spellings and grouped
// differently (Claude: one write+shell matcher; Kiro: one hook per kind): it must come
// back as one set of shared hooks that every agent, Codex included, gets.
func TestSameHooksFromClaudeAndKiroAreSharedByEveryAgent(t *testing.T) {
	home := setup(t)
	write := func(rel, content string) {
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".claude/settings.json", `{"hooks": {"PreToolUse": [{"matcher": "Edit|MultiEdit|Write|NotebookEdit|Bash|PowerShell", "hooks": [{"type": "command", "command": "C:/Users/me/node.exe \"$HOME/g/dispatch.js\" --harness claude"}]}], "Stop": [{"hooks": [{"type": "command", "command": "notify-desktop.js"}]}]}}`)
	write(".kiro/hooks/g.json", `{"version": "v1", "hooks": [
	  {"name": "gw", "trigger": "PreToolUse", "matcher": "write", "action": {"type": "command", "command": "C:/Users/me/node.exe \"C:/Users/me/g/dispatch.js\" --harness kiro"}},
	  {"name": "gs", "trigger": "PreToolUse", "matcher": "shell", "action": {"type": "command", "command": "C:/Users/me/node.exe \"C:/Users/me/g/dispatch.js\" --harness kiro"}}]}`)
	src := filepath.Join(t.TempDir(), "src")
	r := call("import", "all", "--source", src)
	if r.code != 0 {
		t.Fatalf("%d\n%s%s", r.code, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stdout, "now shared by every agent") || !strings.Contains(r.stdout, "1 hooks only claude has stay scoped") {
		t.Errorf("warnings:\n%s", r.stdout)
	}
	hooks, _ := os.ReadFile(filepath.Join(src, "hooks.yaml"))
	if got := strings.Count(string(hooks), `{{home}}/node.exe "{{home}}/g/dispatch.js" --harness {{agent}}`); got != 2 {
		t.Errorf("want the write and the shell guard once each, templated:\n%s", hooks)
	}

	fresh := t.TempDir()
	setHome(t, fresh)
	if r := call("install", "all", "--source", src); r.code != 0 {
		t.Fatalf("%d\n%s%s", r.code, r.stdout, r.stderr)
	}
	for agent, rel := range map[string]string{"claude": ".claude/settings.json", "kiro": ".kiro/hooks/nexo.json", "codex": ".codex/hooks.json"} {
		b, err := os.ReadFile(filepath.Join(fresh, rel))
		if err != nil {
			t.Fatalf("%s: %v", agent, err)
		}
		want := filepath.ToSlash(fresh) + `/g/dispatch.js\" --harness ` + agent
		if strings.Count(string(b), want) != 2 {
			t.Errorf("%s should get both guards with its own harness and this home (%s):\n%s", agent, want, b)
		}
		if (agent == "claude") != strings.Contains(string(b), "notify-desktop.js") {
			t.Errorf("%s: the Claude-only hook must stay in Claude:\n%s", agent, b)
		}
	}
}

func TestHookCommand(t *testing.T) {
	setup(t)
	dir := t.TempDir()
	hooksFile := filepath.Join(dir, "hooks.json")
	// The hook runs through the system shell: sh on Unix, cmd.exe on Windows.
	command := "echo no >&2; exit 2"
	if runtime.GOOS == "windows" {
		command = "echo no 1>&2 & exit 2"
	}
	hooks, _ := json.Marshal([]map[string]string{{"name": "g", "event": "PreToolUse", "matcher": "write", "command": command}})
	if err := os.WriteFile(hooksFile, hooks, 0o644); err != nil {
		t.Fatal(err)
	}
	callIn := func(stdin string, args ...string) result {
		var out, errOut bytes.Buffer
		code := (cli{stdout: &out, stderr: &errOut}).hook(args, strings.NewReader(stdin))
		return result{code, out.String(), errOut.String()}
	}
	if r := callIn(`{"hook":"tool.execute.before","tool":"write"}`, "opencode", "--hooks", hooksFile); r.code != 0 || strings.TrimSpace(r.stdout) != `{"block":"no"}` {
		t.Errorf("block: %+v", r)
	}
	if r := callIn(`{"hook":"tool.execute.before","tool":"bash"}`, "opencode", "--hooks", hooksFile); r.code != 0 || strings.TrimSpace(r.stdout) != `{}` {
		t.Errorf("no match: %+v", r)
	}
	if r := callIn(`{}`, "opencode", "--hooks", filepath.Join(dir, "missing.json")); r.code != 0 || strings.TrimSpace(r.stdout) != `{}` || r.stderr == "" {
		t.Errorf("a missing hooks file fails open: %+v", r)
	}
	if r := callIn(`{}`, "claude", "--hooks", hooksFile); r.code != 2 {
		t.Errorf("only opencode: %+v", r)
	}
}

// setHome points every home lookup at dir: os.UserHomeDir reads HOME on Unix and
// USERPROFILE on Windows.
func setHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}
