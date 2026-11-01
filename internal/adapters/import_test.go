package adapters

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/Madian2112/nexo-ai/internal/install"
	"github.com/Madian2112/nexo-ai/internal/model"
	"github.com/Madian2112/nexo-ai/internal/plan"
)

// installed writes the example source for agent into a fresh home and returns it.
func installed(t *testing.T, agent string) string {
	t.Helper()
	home := t.TempDir()
	p := planFor(t, agent, home)
	if _, err := install.Apply(p, install.Options{Home: home, StateDir: filepath.Join(home, "state"), BackupDir: filepath.Join(home, "bk")}); err != nil {
		t.Fatal(err)
	}
	return home
}

func importFrom(t *testing.T, agent, home string) (*model.Source, []string) {
	t.Helper()
	f, ok := GetImport(agent)
	if !ok {
		t.Fatalf("no importer for %s", agent)
	}
	src, warn, err := f(plan.OSEnv(home))
	if err != nil {
		t.Fatalf("import %s: %v", agent, err)
	}
	return src, warn
}

func names[T any](items []T, name func(T) string) string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = name(it)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func permSet(ps []model.Permission) string {
	var out []string
	for _, p := range ps {
		for _, m := range p.Match {
			out = append(out, p.Effect+" "+p.Capability+" "+m)
		}
	}
	sort.Strings(out)
	return strings.Join(out, "|")
}

func TestRoundTrip(t *testing.T) {
	example := loadExample(t)
	for _, agent := range Names() {
		t.Run(agent, func(t *testing.T) {
			got, _ := importFrom(t, agent, installed(t, agent))

			if names(got.MCP, func(s model.MCPServer) string { return s.Name }) != "engram,microsoft-learn" {
				t.Errorf("mcp: %+v", got.MCP)
			}
			if got.MCP[1].URL != "https://learn.microsoft.com/api/mcp" || got.MCP[0].Command != "engram" || strings.Join(got.MCP[0].Args, " ") != "mcp" {
				t.Errorf("mcp fields: %+v", got.MCP)
			}
			if len(got.Skills) != 1 || got.Skills[0].Name != "tdd" {
				t.Errorf("skills: %+v", got.Skills)
			}
			if len(got.Agents) != 1 || got.Agents[0].Name != "dotnet-pro" || !strings.HasPrefix(got.Agents[0].Prompt, "You are a senior") {
				t.Errorf("agents: %+v", got.Agents)
			}
			if !strings.Contains(got.Soul.Core, "Reply in the user's language") {
				t.Errorf("core: %q", got.Soul.Core)
			}
			if len(got.Hooks) != 2 {
				t.Fatalf("hooks: %+v", got.Hooks)
			}
			for _, h := range got.Hooks {
				if h.Command != "node ~/agent-guards/bin/dispatch.js --harness "+agent {
					t.Errorf("hook command: %q", h.Command)
				}
				if h.Event == model.EventPreToolUse && h.Matcher != model.MatcherWrite {
					t.Errorf("matcher: %+v", h)
				}
			}
			if agent != "codex" && permSet(got.Permissions) != permSet(example.Permissions) {
				t.Errorf("permissions:\n got %s\nwant %s", permSet(got.Permissions), permSet(example.Permissions))
			}
			if agent == "codex" && len(got.Permissions) != 0 {
				t.Error("Codex has no permission rules to import")
			}
		})
	}
}

func TestRoundTripSections(t *testing.T) {
	claude, _ := importFrom(t, "claude", installed(t, "claude"))
	if len(claude.Soul.Sections) != 1 || claude.Soul.Sections[0].Name != "stack-dotnet" || claude.Soul.Sections[0].Inclusion != model.InclusionFileMatch || len(claude.Soul.Sections[0].Globs) != 3 {
		t.Errorf("claude rule back to a fileMatch section: %+v", claude.Soul.Sections)
	}
	kiro, _ := importFrom(t, "kiro", installed(t, "kiro"))
	got := map[string]model.Inclusion{}
	for _, s := range kiro.Soul.Sections {
		got[s.Name] = s.Inclusion
	}
	if got["persona"] != model.InclusionAlways || got["stack-dotnet"] != model.InclusionFileMatch || strings.TrimSpace(kiro.Soul.Core) == "" {
		t.Errorf("kiro steering: %v core=%q", got, kiro.Soul.Core)
	}
}

func TestImportNativeOnlyConfig(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, ".claude", "settings.json"), `{
	  "hooks": {
	    "PreToolUse": [
	      {"matcher": "Bash", "hooks": [{"type": "command", "command": "guard.sh", "timeout": 5}]},
	      {"matcher": "Bash|Edit", "hooks": [{"type": "command", "command": "mixed.sh"}]},
	      {"matcher": "Bash|WebFetch", "hooks": [{"type": "command", "command": "web.sh"}]}
	    ],
	    "SubagentStart": [{"hooks": [{"type": "command", "command": "x"}]}]
	  },
	  "permissions": {"allow": ["Bash", "Bash(npm test)", "WebFetch(domain:x.com)", "mcp__engram__mem_save"], "deny": ["Read(./.env)"]}
	}`)
	write(t, filepath.Join(home, ".claude.json"), `{"mcpServers": {"off": {"command": "x", "disabled": true}, "bad": {}}}`)
	src, warn := importFrom(t, "claude", home)

	if len(src.Hooks) != 3 || src.Hooks[0].Matcher != model.MatcherShell || src.Hooks[0].Timeout != 5 {
		t.Fatalf("hooks: %+v", src.Hooks)
	}
	if src.Hooks[1].Command != "mixed.sh" || src.Hooks[1].Matcher != model.MatcherWrite || src.Hooks[2].Matcher != model.MatcherShell {
		t.Errorf("a matcher naming write and shell tools is split into one hook per kind: %+v", src.Hooks[1:])
	}
	if permSet(src.Permissions) != "allow mcp engram__mem_save|allow shell *|allow shell npm test|deny read ./.env" {
		t.Errorf("permissions: %s", permSet(src.Permissions))
	}
	if len(src.MCP) != 0 {
		t.Errorf("disabled and invalid servers must be skipped: %+v", src.MCP)
	}
	all := strings.Join(warn, "\n")
	for _, want := range []string{`matcher "Bash|WebFetch" skipped`, "SubagentStart skipped", `"WebFetch(domain:x.com)" skipped`, `"off" skipped: it is disabled`, `"bad" skipped`} {
		if !strings.Contains(all, want) {
			t.Errorf("missing warning %q in:\n%s", want, all)
		}
	}
}

func TestImportKiroManualSteeringAndPromptFile(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, ".kiro", "steering", "release.md"), "---\ninclusion: manual\n---\nRelease steps.")
	write(t, filepath.Join(home, ".kiro", "prompts", "a.md"), "Prompt from file.")
	write(t, filepath.Join(home, ".kiro", "agents", "a.json"), `{"description": "d", "prompt": "file://../prompts/a.md", "tools": ["@builtin", "@ghost"]}`)
	write(t, filepath.Join(home, ".kiro", "hooks", "x.json"), `{"hooks": [{"name": "t", "trigger": "PostTaskExec", "action": {"type": "command", "command": "x"}}]}`)
	src, warn := importFrom(t, "kiro", home)

	if len(src.Soul.Sections) != 1 || src.Soul.Sections[0].Inclusion != model.InclusionIntent || src.Soul.Sections[0].Keywords[0] != "release" {
		t.Errorf("manual steering → intent: %+v", src.Soul.Sections)
	}
	if len(src.Agents) != 1 || src.Agents[0].Name != "a" || src.Agents[0].Prompt != "Prompt from file." || len(src.Agents[0].MCP) != 0 {
		t.Errorf("agent: %+v", src.Agents)
	}
	all := strings.Join(warn, "\n")
	if !strings.Contains(all, "@ghost is not a defined MCP server") || !strings.Contains(all, "PostTaskExec has no canonical event") {
		t.Errorf("warnings:\n%s", all)
	}
}

func TestImportInvalidSharedFileFails(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, ".codex", "config.toml"), "not = [toml")
	f, _ := GetImport("codex")
	if _, _, err := f(plan.OSEnv(home)); err == nil {
		t.Fatal("an unreadable config must be an error, not an empty import")
	}
}

func TestKiroPromptOutsideKiroDirIsRefused(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, "outside.md"), "secret notes")
	write(t, filepath.Join(home, ".kiro", "agents", "a.json"), `{"description": "d", "prompt": "file://../../outside.md"}`)
	src, warn := importFrom(t, "kiro", home)
	if len(src.Agents) != 0 || !strings.Contains(strings.Join(warn, "\n"), "is outside") {
		t.Fatalf("agents=%+v warn=%v", src.Agents, warn)
	}
}

func TestKiroPromptSymlinkEscapeIsRefused(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, "outside.md"), "secret notes")
	write(t, filepath.Join(home, ".kiro", "agents", "a.json"), `{"description": "d", "prompt": "file://../prompts/a.md"}`)
	if err := os.MkdirAll(filepath.Join(home, ".kiro", "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "outside.md"), filepath.Join(home, ".kiro", "prompts", "a.md")); err != nil {
		t.Fatal(err)
	}
	src, warn := importFrom(t, "kiro", home)
	if len(src.Agents) != 0 || !strings.Contains(strings.Join(warn, "\n"), "is outside") {
		t.Fatalf("agents=%+v warn=%v", src.Agents, warn)
	}
}
