package source

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Madian2112/nexo-ai/internal/model"
)

func exampleDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs("../../source-example")
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoadExample(t *testing.T) {
	src, err := Load(exampleDir(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !src.Settings.KiroToolSearch {
		t.Error("kiro.toolSearch should be true")
	}
	if !strings.Contains(src.Soul.Core, "Reply in the user's language") {
		t.Errorf("core not loaded: %q", src.Soul.Core)
	}
	byName := map[string]model.Section{}
	for _, s := range src.Soul.Sections {
		byName[s.Name] = s
	}
	dotnet := byName["stack-dotnet"]
	if dotnet.Inclusion != model.InclusionFileMatch || len(dotnet.Globs) != 3 {
		t.Errorf("stack-dotnet: %+v", dotnet)
	}
	if strings.Contains(dotnet.Body, "inclusion:") {
		t.Error("frontmatter leaked into body")
	}
	if byName["code-review"].Inclusion != model.InclusionIntent || len(byName["code-review"].Keywords) != 4 {
		t.Errorf("code-review: %+v", byName["code-review"])
	}
	if len(src.Skills) != 1 || src.Skills[0].Name != "tdd" || !filepath.IsAbs(src.Skills[0].Dir) {
		t.Errorf("skills: %+v", src.Skills)
	}
	if len(src.MCP) != 2 || src.MCP[0].Name != "engram" || !src.MCP[1].Remote() {
		t.Errorf("mcp (sorted by name): %+v", src.MCP)
	}
	if len(src.Hooks) != 2 || src.Hooks[0].Matcher != model.MatcherWrite {
		t.Errorf("hooks: %+v", src.Hooks)
	}
	if len(src.Permissions) != 4 {
		t.Errorf("permissions: %+v", src.Permissions)
	}
	if len(src.Agents) != 1 || src.Agents[0].Name != "dotnet-pro" || len(src.Agents[0].MCP) != 2 || !strings.HasPrefix(src.Agents[0].Prompt, "You are") {
		t.Errorf("agents: %+v", src.Agents)
	}
}

func TestLoadEmptyDirIsValid(t *testing.T) {
	src, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if src.Soul.Core != "" || len(src.Skills) != 0 {
		t.Errorf("expected empty source, got %+v", src)
	}
}

func TestLoadMissingDirFails(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected error for missing source dir")
	}
}

func TestValidationErrors(t *testing.T) {
	cases := map[string]struct{ path, content, want string }{
		"skill without description": {"skills/x/SKILL.md", "---\nname: x\n---\nbody", "description"},
		"bad inclusion":             {"soul/sections/a.md", "---\ninclusion: sometimes\n---\nbody", "inclusion"},
		"fileMatch without globs":   {"soul/sections/a.md", "---\ninclusion: fileMatch\n---\nbody", "globs"},
		"bad effect":                {"permissions.yaml", "rules:\n  - capability: read\n    match: [a]\n    effect: maybe\n", "effect"},
		"bad capability":            {"permissions.yaml", "rules:\n  - capability: fly\n    match: [a]\n    effect: deny\n", "capability"},
		"bad event":                 {"hooks.yaml", "hooks:\n  - name: h\n    event: Whenever\n    command: x\n", "event"},
		"bad matcher":               {"hooks.yaml", "hooks:\n  - name: h\n    event: PreToolUse\n    matcher: fly\n    command: x\n", "matcher"},
		"mcp without command":       {"mcp.yaml", "servers:\n  a: {}\n", "command"},
		"agent unknown mcp":         {"agents/a.md", "---\nname: a\ndescription: d\nmcp: [ghost]\n---\nbody", "ghost"},
		"section named core":        {"soul/sections/core.md", "body", `"core" is already used by soul/core.md`},
		"agent name escapes":        {"agents/a.md", "---\nname: ../CLAUDE\ndescription: d\n---\nbody", "invalid name"},
		"hook agent escapes":        {"hooks.yaml", "hooks:\n  - name: h\n    event: Stop\n    command: x\n    agents: [../x]\n", "agents"},
		"hook agent typo":           {"hooks.yaml", "hooks:\n  - name: h\n    event: Stop\n    command: x\n    agents: [claudecode]\n", `unknown agent "claudecode"`},
		"model for unknown tool":    {"nexo.yaml", "models:\n  opus:\n    cursor: x\n", `unknown tool "cursor"`},
		"hookAgent unknown tool":    {"nexo.yaml", "hookAgent:\n  cursor: claude\n", `unknown tool "cursor"`},
		"hookAgent bad value":       {"nexo.yaml", "hookAgent:\n  opencode: ../x\n", "hookAgent.opencode"},
		"skill name escapes":        {"skills/x/SKILL.md", "---\nname: ..\ndescription: d\n---\nbody", "invalid name"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, c.path)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(c.content), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := Load(dir)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want error containing %q, got %v", c.want, err)
			}
			if !strings.Contains(err.Error(), filepath.FromSlash(c.path)) {
				t.Errorf("error should name the file %q: %v", c.path, err)
			}
		})
	}
}

func TestDuplicateNames(t *testing.T) {
	cases := map[string][2][2]string{
		"skills": {{"skills/a/SKILL.md", "---\nname: same\ndescription: d\n---\n"}, {"skills/b/SKILL.md", "---\nname: same\ndescription: d\n---\n"}},
		"agents": {{"agents/a.md", "---\nname: same\ndescription: d\n---\n"}, {"agents/b.md", "---\nname: same\ndescription: d\n---\n"}},
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range files {
				p := filepath.Join(dir, f[0])
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(f[1]), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), `"same" is already used`) {
				t.Fatalf("want duplicate error, got %v", err)
			}
		})
	}
}

func TestFrontmatterCRLF(t *testing.T) {
	meta, body, err := SplitFrontmatter([]byte("---\r\nname: x\r\n---\r\nhello\r\n"))
	if err != nil || !strings.Contains(meta, "name: x") || strings.TrimSpace(body) != "hello" {
		t.Fatalf("meta=%q body=%q err=%v", meta, body, err)
	}
}
