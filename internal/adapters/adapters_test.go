package adapters

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/Madian2112/nexo-ai/internal/install"
	"github.com/Madian2112/nexo-ai/internal/model"
	"github.com/Madian2112/nexo-ai/internal/plan"
	"github.com/Madian2112/nexo-ai/internal/source"
)

func loadExample(t *testing.T) *model.Source {
	t.Helper()
	src, err := source.Load("../../source-example")
	if err != nil {
		t.Fatal(err)
	}
	return src
}

func planFor(t *testing.T, agent string, home string) plan.Plan {
	t.Helper()
	f, ok := Get(agent)
	if !ok {
		t.Fatalf("adapter %q not registered", agent)
	}
	p, err := f(loadExample(t), plan.OSEnv(home))
	if err != nil {
		t.Fatalf("plan %s: %v", agent, err)
	}
	return p
}

func op(t *testing.T, p plan.Plan, rel string, home string) plan.Op {
	t.Helper()
	want := filepath.Join(home, filepath.FromSlash(rel))
	for _, o := range p.Ops {
		if o.Path == want {
			return o
		}
	}
	paths := []string{}
	for _, o := range p.Ops {
		paths = append(paths, o.Path)
	}
	t.Fatalf("no op for %s; ops: %v", rel, paths)
	return plan.Op{}
}

func noOp(t *testing.T, p plan.Plan, rel string, home string) {
	t.Helper()
	want := filepath.Join(home, filepath.FromSlash(rel))
	for _, o := range p.Ops {
		if o.Path == want {
			t.Fatalf("unexpected op for %s", rel)
		}
	}
}

func jsonOf(t *testing.T, o plan.Op) map[string]any {
	t.Helper()
	m := map[string]any{}
	if err := json.Unmarshal(o.Content, &m); err != nil {
		t.Fatalf("%s: %v", o.Path, err)
	}
	return m
}

func hasWarning(p plan.Plan, substr string) bool {
	for _, w := range p.Warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

func TestNames(t *testing.T) {
	if got := strings.Join(Names(), ","); got != "claude,codex,kiro,opencode" || got != strings.Join(model.AgentNames, ",") {
		t.Fatalf("Names() = %s, model.AgentNames = %v", got, model.AgentNames)
	}
}

func TestClaude(t *testing.T) {
	home := t.TempDir()
	p := planFor(t, "claude", home)

	soul := string(op(t, p, ".claude/CLAUDE.md", home).Content)
	if !strings.Contains(soul, "Reply in the user's language") || !strings.Contains(soul, "# Persona") {
		t.Errorf("CLAUDE.md should hold core + always sections:\n%s", soul)
	}
	if strings.Contains(soul, ".NET rules") || strings.Contains(soul, "Code review") {
		t.Error("CLAUDE.md must not contain fileMatch or intent sections")
	}
	rule := string(op(t, p, ".claude/rules/nexo-stack-dotnet.md", home).Content)
	if !strings.HasPrefix(rule, "---\npaths:\n  - \"**/*.cs\"\n") || !strings.Contains(rule, "Async all the way") {
		t.Errorf("rule:\n%s", rule)
	}
	noOp(t, p, ".claude/rules/nexo-code-review.md", home)
	if !hasWarning(p, `"code-review" (intent)`) {
		t.Errorf("missing intent warning: %v", p.Warnings)
	}

	link := op(t, p, ".claude/skills/tdd", home)
	if !link.IsLink() || !strings.HasSuffix(link.LinkTo, filepath.Join("skills", "tdd")) {
		t.Errorf("skill link: %+v", link)
	}
	agent := string(op(t, p, ".claude/agents/dotnet-pro.md", home).Content)
	if !strings.Contains(agent, "name: dotnet-pro\n") || !strings.Contains(agent, "You are a senior") {
		t.Errorf("agent:\n%s", agent)
	}

	settings := jsonOf(t, op(t, p, ".claude/settings.json", home))
	hooks := settings["hooks"].(map[string]any)
	pre := hooks["PreToolUse"].([]any)[0].(map[string]any)
	if pre["matcher"] != "Edit|MultiEdit|Write|NotebookEdit" {
		t.Errorf("matcher: %v", pre["matcher"])
	}
	cmd := pre["hooks"].([]any)[0].(map[string]any)["command"]
	if cmd != "node ~/agent-guards/bin/dispatch.js --harness claude" {
		t.Errorf("{{agent}} not substituted: %v", cmd)
	}
	if _, ok := hooks["UserPromptSubmit"].([]any)[0].(map[string]any)["matcher"]; ok {
		t.Error("hook without matcher must not emit one")
	}
	perms := settings["permissions"].(map[string]any)
	if !containsAll(perms["deny"], "Read(**/.env)", "Edit(**/secrets/**)") || !containsAll(perms["allow"], "Bash(git status*)") || !containsAll(perms["ask"], "Bash(git push*)") {
		t.Errorf("permissions: %v", perms)
	}

	mcp := jsonOf(t, op(t, p, ".claude.json", home))["mcpServers"].(map[string]any)
	if mcp["engram"].(map[string]any)["type"] != "stdio" || mcp["microsoft-learn"].(map[string]any)["type"] != "http" {
		t.Errorf("mcp: %v", mcp)
	}
}

func TestClaudePreservesForeignKeys(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, ".claude.json"), `{"numStartups": 42, "mcpServers": {"old": {}}}`)
	write(t, filepath.Join(home, ".claude", "settings.json"), `{"model": "opus", "permissions": {"defaultMode": "auto", "allow": ["Old"]}}`)
	p := planFor(t, "claude", home)

	state := jsonOf(t, op(t, p, ".claude.json", home))
	if state["numStartups"] != float64(42) {
		t.Errorf("foreign key lost: %v", state)
	}
	if _, ok := state["mcpServers"].(map[string]any)["old"]; ok {
		t.Error("mcpServers is owned by nexo-ai and must be replaced from the source")
	}
	settings := jsonOf(t, op(t, p, ".claude/settings.json", home))
	perms := settings["permissions"].(map[string]any)
	if settings["model"] != "opus" || perms["defaultMode"] != "auto" || containsAll(perms["allow"], "Old") {
		t.Errorf("settings merge: %v", settings)
	}
}

func TestInvalidSharedFileIsRefused(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, ".claude.json"), `{not json`)
	f, _ := Get("claude")
	if _, err := f(loadExample(t), plan.OSEnv(home)); err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("want refusal, got %v", err)
	}
}

func TestKiro(t *testing.T) {
	home := t.TempDir()
	p := planFor(t, "kiro", home)

	core := string(op(t, p, ".kiro/steering/nexo-core.md", home).Content)
	if !strings.HasPrefix(core, "---\ninclusion: always\n---\n") {
		t.Errorf("core steering:\n%s", core)
	}
	dotnet := string(op(t, p, ".kiro/steering/nexo-stack-dotnet.md", home).Content)
	if !strings.Contains(dotnet, "inclusion: fileMatch\nfileMatchPattern: [\"**/*.cs\", \"**/*.csproj\", \"**/*.sln\"]\n") {
		t.Errorf("fileMatch steering:\n%s", dotnet)
	}
	noOp(t, p, ".kiro/steering/nexo-code-review.md", home)

	if !op(t, p, ".kiro/skills/tdd", home).IsLink() {
		t.Error("skill should be a link")
	}
	agent := jsonOf(t, op(t, p, ".kiro/agents/dotnet-pro.json", home))
	if agent["prompt"] != "file://../prompts/dotnet-pro.md" || !containsAll(agent["tools"], "@builtin", "@engram", "@microsoft-learn") {
		t.Errorf("agent: %v", agent)
	}
	if !strings.Contains(string(op(t, p, ".kiro/prompts/dotnet-pro.md", home).Content), "You are a senior") {
		t.Error("agent prompt file missing")
	}

	hooks := jsonOf(t, op(t, p, ".kiro/hooks/nexo.json", home))
	first := hooks["hooks"].([]any)[0].(map[string]any)
	if hooks["version"] != "v1" || first["trigger"] != "PreToolUse" || first["matcher"] != "write" {
		t.Errorf("hooks: %v", hooks)
	}
	if first["action"].(map[string]any)["command"] != "node ~/agent-guards/bin/dispatch.js --harness kiro" {
		t.Errorf("command: %v", first["action"])
	}

	perms := string(op(t, p, ".kiro/settings/permissions.yaml", home).Content)
	if !strings.Contains(perms, "capability: fs_read") || !strings.Contains(perms, "capability: shell") {
		t.Errorf("permissions:\n%s", perms)
	}
	mcp := jsonOf(t, op(t, p, ".kiro/settings/mcp.json", home))["mcpServers"].(map[string]any)
	if mcp["engram"].(map[string]any)["command"] != "engram" || mcp["microsoft-learn"].(map[string]any)["url"] == nil {
		t.Errorf("mcp: %v", mcp)
	}
	if jsonOf(t, op(t, p, ".kiro/settings/cli.json", home))["toolSearch.enabled"] != true {
		t.Error("Kiro Tool Search must be enabled")
	}
}

func TestKiroToolSearchOff(t *testing.T) {
	home := t.TempDir()
	src := loadExample(t)
	src.Settings.KiroToolSearch = false
	f, _ := Get("kiro")
	p, err := f(src, plan.OSEnv(home))
	if err != nil {
		t.Fatal(err)
	}
	noOp(t, p, ".kiro/settings/cli.json", home)
}

func TestCodex(t *testing.T) {
	home := t.TempDir()
	p := planFor(t, "codex", home)

	soul := string(op(t, p, ".codex/AGENTS.md", home).Content)
	if !strings.Contains(soul, "# Persona") || strings.Contains(soul, ".NET rules") {
		t.Errorf("AGENTS.md:\n%s", soul)
	}
	if !hasWarning(p, `"stack-dotnet" (fileMatch) skipped`) || !hasWarning(p, "permission rules skipped") || !hasWarning(p, "/hooks") {
		t.Errorf("warnings: %v", p.Warnings)
	}
	if !op(t, p, ".agents/skills/tdd", home).IsLink() {
		t.Error("skill should be linked into ~/.agents/skills")
	}

	var agent map[string]any
	if err := toml.Unmarshal(op(t, p, ".codex/agents/dotnet-pro.toml", home).Content, &agent); err != nil {
		t.Fatal(err)
	}
	if agent["name"] != "dotnet-pro" || !strings.HasPrefix(agent["developer_instructions"].(string), "You are a senior") {
		t.Errorf("agent: %v", agent)
	}

	hooks := jsonOf(t, op(t, p, ".codex/hooks.json", home))["hooks"].(map[string]any)
	pre := hooks["PreToolUse"].([]any)[0].(map[string]any)
	if pre["matcher"] != "apply_patch|Edit|Write" {
		t.Errorf("matcher: %v", pre["matcher"])
	}

	var cfg map[string]any
	if err := toml.Unmarshal(op(t, p, ".codex/config.toml", home).Content, &cfg); err != nil {
		t.Fatal(err)
	}
	servers := cfg["mcp_servers"].(map[string]any)
	if servers["engram"].(map[string]any)["command"] != "engram" || servers["microsoft-learn"].(map[string]any)["url"] == nil {
		t.Errorf("mcp_servers: %v", servers)
	}
}

func TestCodexPreservesConfig(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, ".codex", "config.toml"), "# mine\nmodel = \"gpt-5.6\"\n\n[mcp_servers.old]\ncommand = \"x\"\n")
	p := planFor(t, "codex", home)
	var cfg map[string]any
	if err := toml.Unmarshal(op(t, p, ".codex/config.toml", home).Content, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["model"] != "gpt-5.6" {
		t.Errorf("foreign key lost: %v", cfg)
	}
	if _, ok := cfg["mcp_servers"].(map[string]any)["old"]; ok {
		t.Error("mcp_servers must be replaced from the source")
	}
	if !hasWarning(p, "without its comments") {
		t.Errorf("warnings: %v", p.Warnings)
	}
}

// sharedFiles lists, per agent, each shared file and the keys a full install owns in it.
var sharedFiles = map[string]map[string][]string{
	"claude": {".claude/settings.json": {"hooks", "permissions.rules"}, ".claude.json": {"mcpServers"}},
	"kiro":   {".kiro/settings/mcp.json": {"mcpServers"}, ".kiro/settings/cli.json": {"toolSearch.enabled"}},
	"codex":  {".codex/config.toml": {"mcp_servers"}},
}

func TestFullInstallOwnsExpectedKeys(t *testing.T) {
	for agent, files := range sharedFiles {
		home := t.TempDir()
		p := planFor(t, agent, home)
		for rel, keys := range files {
			if got := strings.Join(op(t, p, rel, home).Keys, ","); got != strings.Join(keys, ",") {
				t.Errorf("%s %s keys = %s, want %v", agent, rel, got, keys)
			}
		}
	}
}

func TestDroppedCategoriesRemoveOwnedKeysOnly(t *testing.T) {
	user := map[string]string{
		".claude/settings.json":   `{"model": "opus", "hooks": {"Stop": []}, "permissions": {"defaultMode": "auto", "allow": ["Bash(x)"]}}`,
		".claude.json":            `{"numStartups": 1, "mcpServers": {"engram": {}}}`,
		".kiro/settings/mcp.json": `{"mcpServers": {"engram": {}}, "other": 1}`,
		".kiro/settings/cli.json": `{"toolSearch.enabled": true, "memory.mode": "disabled"}`,
		".codex/config.toml":      "model = \"gpt\"\n\n[mcp_servers.engram]\ncommand = \"engram\"\n",
	}
	empty := &model.Source{}
	for agent, files := range sharedFiles {
		t.Run(agent, func(t *testing.T) {
			home := t.TempDir()
			owned := map[string][]string{}
			for rel, keys := range files {
				write(t, filepath.Join(home, rel), user[rel])
				owned[filepath.Join(home, rel)] = keys
			}
			f, _ := Get(agent)

			// Never installed before: the user's values are not nexo-ai's to remove.
			p, err := f(empty, plan.OSEnv(home))
			if err != nil || len(p.Ops) != 0 {
				t.Fatalf("ops without previous ownership: %+v %v", p.Ops, err)
			}

			env := plan.OSEnv(home)
			env.OwnedKeys = owned
			p, err = f(empty, env)
			if err != nil {
				t.Fatal(err)
			}
			for rel, keys := range files {
				o := op(t, p, rel, home)
				if len(o.Keys) != 0 {
					t.Errorf("%s: no keys should stay owned, got %v", rel, o.Keys)
				}
				content := string(o.Content)
				for _, k := range keys {
					k = strings.TrimSuffix(k, ".rules")
					if k == "permissions" {
						if strings.Contains(content, "Bash(x)") || !strings.Contains(content, "defaultMode") {
							t.Errorf("%s: allow/ask/deny must go, defaultMode must stay:\n%s", rel, content)
						}
						continue
					}
					if strings.Contains(content, k) {
						t.Errorf("%s: owned key %s should be dropped:\n%s", rel, k, content)
					}
				}
				for _, foreign := range []string{"opus", "numStartups", "other", "memory.mode", "gpt"} {
					if strings.Contains(user[rel], foreign) && !strings.Contains(content, foreign) {
						t.Errorf("%s: foreign value %s lost:\n%s", rel, foreign, content)
					}
				}
			}
		})
	}
}

func containsAll(list any, want ...string) bool {
	items := map[string]bool{}
	switch l := list.(type) {
	case []any:
		for _, it := range l {
			if s, ok := it.(string); ok {
				items[s] = true
			}
		}
	case []string:
		for _, s := range l {
			items[s] = true
		}
	}
	for _, w := range want {
		if !items[w] {
			return false
		}
	}
	return true
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestHookAgentsScope(t *testing.T) {
	src := &model.Source{Hooks: []model.Hook{
		{Name: "a", Event: model.EventSessionStart, Command: "claude-only.sh", Agents: []string{"claude"}},
		{Name: "b", Event: model.EventSessionStart, Command: "kiro-only.sh", Agents: []string{"kiro"}},
		{Name: "c", Event: model.EventSessionStart, Command: "everyone.sh"},
	}}
	for agent, want := range map[string][]string{
		"claude": {"claude-only.sh", "everyone.sh"},
		"kiro":   {"kiro-only.sh", "everyone.sh"},
		"codex":  {"everyone.sh"},
	} {
		home := t.TempDir()
		f, _ := Get(agent)
		p, err := f(src, plan.OSEnv(home))
		if err != nil {
			t.Fatal(err)
		}
		all := ""
		for _, o := range p.Ops {
			all += string(o.Content)
		}
		for _, cmd := range []string{"claude-only.sh", "kiro-only.sh", "everyone.sh"} {
			if strings.Contains(all, cmd) != containsAll(want, cmd) {
				t.Errorf("%s: hook %s installed=%v, want %v", agent, cmd, strings.Contains(all, cmd), want)
			}
		}
	}
}

func TestHooksOnlyForOtherAgentsWriteNoClaudeHooksKey(t *testing.T) {
	home := t.TempDir()
	src := &model.Source{Hooks: []model.Hook{{Name: "k", Event: model.EventStop, Command: "x", Agents: []string{"kiro"}}}}
	f, _ := Get("claude")
	p, err := f(src, plan.OSEnv(home))
	if err != nil || len(p.Ops) != 0 {
		t.Fatalf("no settings.json should be written: %+v %v", p.Ops, err)
	}
}

func envSource() *model.Source {
	return &model.Source{MCP: []model.MCPServer{{
		Name:    "tfs",
		Command: "node",
		Args:    []string{"{{home}}/.claude/mcp/tfs/server.js"},
		Env:     map[string]string{"TFS_BASE": "{{env:TFS_BASE}}", "TFS_URL": "{{env:TFS_BASE}}/api", "MODE": "ro"},
	}}}
}

func TestMCPEnvReferencesPerAgent(t *testing.T) {
	cases := map[string]struct{ rel, want string }{
		"claude": {".claude.json", `"TFS_BASE": "${TFS_BASE}"`},
		"kiro":   {".kiro/settings/mcp.json", `"TFS_BASE": "${TFS_BASE}"`},
	}
	for agent, c := range cases {
		home := t.TempDir()
		f, _ := Get(agent)
		p, err := f(envSource(), plan.OSEnv(home))
		if err != nil {
			t.Fatal(err)
		}
		content := string(op(t, p, c.rel, home).Content)
		for _, want := range []string{c.want, `"TFS_URL": "${TFS_BASE}/api"`, filepath.ToSlash(home) + "/.claude/mcp/tfs/server.js", `"MODE": "ro"`} {
			if !strings.Contains(content, want) {
				t.Errorf("%s: missing %s in:\n%s", agent, want, content)
			}
		}
		if strings.Contains(content, "{{") {
			t.Errorf("%s: unresolved placeholder:\n%s", agent, content)
		}
	}
}

func TestCodexPassesEnvThrough(t *testing.T) {
	home := t.TempDir()
	f, _ := Get("codex")
	p, err := f(envSource(), plan.OSEnv(home))
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := toml.Unmarshal(op(t, p, ".codex/config.toml", home).Content, &cfg); err != nil {
		t.Fatal(err)
	}
	tfs := cfg["mcp_servers"].(map[string]any)["tfs"].(map[string]any)
	if !containsAll(tfs["env_vars"], "TFS_BASE") {
		t.Errorf("env_vars: %v", tfs["env_vars"])
	}
	env := tfs["env"].(map[string]any)
	if env["MODE"] != "ro" || env["TFS_BASE"] != nil || env["TFS_URL"] != nil {
		t.Errorf("env: %v", env)
	}
	if !hasWarning(p, `TFS_URL not written`) {
		t.Errorf("warnings: %v", p.Warnings)
	}
	if strings.Contains(string(op(t, p, ".codex/config.toml", home).Content), "{{") {
		t.Error("no placeholder may reach config.toml")
	}
}

func TestEnvReferencesComeBackOnImport(t *testing.T) {
	for _, agent := range []string{"claude", "kiro", "codex"} {
		home := t.TempDir()
		f, _ := Get(agent)
		p, err := f(envSource(), plan.OSEnv(home))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := install.Apply(p, install.Options{Home: home, StateDir: filepath.Join(home, "st"), BackupDir: filepath.Join(home, "bk")}); err != nil {
			t.Fatal(err)
		}
		src, _ := importFrom(t, agent, home)
		if len(src.MCP) != 1 || src.MCP[0].Env["TFS_BASE"] != "{{env:TFS_BASE}}" {
			t.Errorf("%s: %+v", agent, src.MCP)
		}
	}
}

func TestKiroStopHook(t *testing.T) {
	home := t.TempDir()
	src := &model.Source{Hooks: []model.Hook{{Name: "end", Event: model.EventStop, Command: "end.sh"}}}
	f, _ := Get("kiro")
	p, err := f(src, plan.OSEnv(home))
	if err != nil {
		t.Fatal(err)
	}
	first := jsonOf(t, op(t, p, ".kiro/hooks/nexo.json", home))["hooks"].([]any)[0].(map[string]any)
	if first["trigger"] != "Stop" {
		t.Errorf("hook: %v", first)
	}
}

func TestCodexSkipsServerWithEnvInCommandLine(t *testing.T) {
	home := t.TempDir()
	src := &model.Source{MCP: []model.MCPServer{
		{Name: "tfs", Command: "node", Args: []string{"server.js", "--project", "{{env:TFS_PROJECT}}"}},
		{Name: "ok", Command: "node", Args: []string{"ok.js"}},
	}}
	f, _ := Get("codex")
	p, err := f(src, plan.OSEnv(home))
	if err != nil {
		t.Fatal(err)
	}
	content := string(op(t, p, ".codex/config.toml", home).Content)
	if strings.Contains(content, "{{") || strings.Contains(content, "tfs") || !strings.Contains(content, "ok.js") {
		t.Errorf("config.toml:\n%s", content)
	}
	if !hasWarning(p, `"tfs" skipped`) {
		t.Errorf("warnings: %v", p.Warnings)
	}
}

func TestImportKeepsCanonicalEnvReferences(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, ".claude.json"), `{"mcpServers": {"a": {"command": "node", "args": ["{{env:X}}", "${Y}", "{env:Z}"]}}}`)
	src, _ := importFrom(t, "claude", home)
	if got := strings.Join(src.MCP[0].Args, " "); got != "{{env:X}} {{env:Y}} {{env:Z}}" {
		t.Errorf("args: %q", got)
	}
}

func modelSource(m string) *model.Source {
	return &model.Source{
		Settings: model.Settings{Models: map[string]map[string]string{
			"opus": {"claude": "opus", "kiro": "claude-opus-5.5", "opencode": "anthropic/claude-opus-5-5"},
		}},
		Agents: []model.Agent{{Name: "a", Description: "d", Model: m, Prompt: "p"}},
	}
}

func TestModelAliasPerTool(t *testing.T) {
	cases := map[string]struct{ rel, want string }{
		"claude":   {".claude/agents/a.md", "model: opus\n"},
		"kiro":     {".kiro/agents/a.json", `"model": "claude-opus-5.5"`},
		"opencode": {".config/opencode/agents/a.md", "model: anthropic/claude-opus-5-5\n"},
	}
	for agent, c := range cases {
		home := t.TempDir()
		f, _ := Get(agent)
		p, err := f(modelSource("opus"), plan.OSEnv(home))
		if err != nil {
			t.Fatal(err)
		}
		if got := string(op(t, p, c.rel, home).Content); !strings.Contains(got, c.want) {
			t.Errorf("%s: want %q in:\n%s", agent, c.want, got)
		}
	}
	// Codex has no entry for opus: no model is written, with a warning.
	home := t.TempDir()
	f, _ := Get("codex")
	p, err := f(modelSource("opus"), plan.OSEnv(home))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(op(t, p, ".codex/agents/a.toml", home).Content); strings.Contains(got, "model") {
		t.Errorf("codex must not get a model:\n%s", got)
	}
	if !hasWarning(p, `model alias "opus" has no codex model`) {
		t.Errorf("warnings: %v", p.Warnings)
	}
}

func TestModelNotAnAliasIsWrittenAsIs(t *testing.T) {
	home := t.TempDir()
	f, _ := Get("claude")
	p, err := f(modelSource("claude-sonnet-5-5"), plan.OSEnv(home))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(op(t, p, ".claude/agents/a.md", home).Content), "model: claude-sonnet-5-5\n") || len(p.Warnings) != 0 {
		t.Errorf("a model that is not an alias keeps today's behavior: %v", p.Warnings)
	}
}

func TestHookAgentOverride(t *testing.T) {
	src := &model.Source{
		Settings: model.Settings{HookAgent: map[string]string{"opencode": "claude"}},
		Hooks:    []model.Hook{{Name: "g", Event: model.EventPreToolUse, Matcher: model.MatcherWrite, Command: "guard --harness {{agent}}"}},
	}
	for agent, c := range map[string]struct{ rel, want string }{
		"opencode": {".config/opencode/nexo/hooks.json", "guard --harness claude"},
		"claude":   {".claude/settings.json", "guard --harness claude"},
		"kiro":     {".kiro/hooks/nexo.json", "guard --harness kiro"},
		"codex":    {".codex/hooks.json", "guard --harness codex"},
	} {
		home := t.TempDir()
		f, _ := Get(agent)
		p, err := f(src, plan.OSEnv(home))
		if err != nil {
			t.Fatal(err)
		}
		if got := string(op(t, p, c.rel, home).Content); !strings.Contains(got, c.want) {
			t.Errorf("%s: want %q in:\n%s", agent, c.want, got)
		}
	}
}
