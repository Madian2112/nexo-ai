package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Madian2112/nexo-ai/internal/model"
)

func base() *model.Source {
	return &model.Source{
		Soul:        model.Soul{Core: "mine\n", Sections: []model.Section{{Name: "persona", Inclusion: model.InclusionAlways, Body: "my persona"}}},
		MCP:         []model.MCPServer{{Name: "engram", Command: "engram"}},
		Hooks:       []model.Hook{{Name: "guards", Event: model.EventPreToolUse, Matcher: model.MatcherWrite, Command: "guard --harness {{agent}}"}},
		Permissions: []model.Permission{{Capability: model.CapabilityShell, Match: []string{"git status*"}, Effect: model.EffectAllow}},
		Agents:      []model.Agent{{Name: "dev", Description: "d", Prompt: "p"}},
	}
}

func TestMergeKeepsSourceAndAddsNew(t *testing.T) {
	imported := &model.Source{
		Soul: model.Soul{Core: "theirs\n", Sections: []model.Section{
			{Name: "persona", Inclusion: model.InclusionAlways, Body: "their persona"},
			{Name: "sql", Inclusion: model.InclusionAlways, Body: "sql rules"},
		}},
		MCP: []model.MCPServer{{Name: "engram", Command: "other"}, {Name: "roslyn", Command: "roslyn", Env: map[string]string{"TOKEN": "x"}}},
		Hooks: []model.Hook{
			{Name: "guards", Event: model.EventPreToolUse, Matcher: model.MatcherWrite, Command: "guard --harness claude"},
			{Name: "guards", Event: model.EventStop, Command: "notify --from claude"},
		},
		Permissions: []model.Permission{{Capability: model.CapabilityShell, Match: []string{"git status*", "npm test"}, Effect: model.EffectAllow}},
		Agents:      []model.Agent{{Name: "dev", Description: "other", Prompt: "x"}, {Name: "ops", Description: "d", Prompt: "p", MCP: []string{"roslyn", "ghost"}}},
	}
	b := base()
	merged, warn := Merge(b, imported, "claude", Options{})

	if merged.Soul.Core != "mine\n" || merged.Soul.Sections[0].Body != "my persona" {
		t.Error("source entries must win")
	}
	if len(merged.Soul.Sections) != 2 || merged.Soul.Sections[1].Name != "sql" {
		t.Errorf("sections: %+v", merged.Soul.Sections)
	}
	if len(merged.MCP) != 2 || merged.MCP[0].Command != "engram" || merged.MCP[1].Name != "roslyn" {
		t.Errorf("mcp: %+v", merged.MCP)
	}
	if len(merged.Hooks) != 2 || merged.Hooks[1].Name != "guards-2" {
		t.Errorf("{{agent}} hook must dedupe and the new one get a unique name: %+v", merged.Hooks)
	}
	if len(merged.Permissions) != 2 || strings.Join(merged.Permissions[1].Match, ",") != "npm test" {
		t.Errorf("permissions: %+v", merged.Permissions)
	}
	if len(merged.Agents) != 2 || strings.Join(merged.Agents[1].MCP, ",") != "roslyn" {
		t.Errorf("agents: %+v", merged.Agents)
	}
	if len(b.MCP) != 1 || len(b.Hooks) != 1 {
		t.Error("Merge must not mutate its base")
	}
	all := strings.Join(warn, "\n")
	for _, want := range []string{"soul/core.md already exists", `section "persona" already exists`, `MCP server "engram" differs`, "may hold tokens", `agent "dev" already exists`, `"ghost", which is not in the source`, "scoped to claude"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing warning %q in:\n%s", want, all)
		}
	}
}

func TestMergeIntoEmptyTakesCore(t *testing.T) {
	merged, _ := Merge(&model.Source{}, &model.Source{Soul: model.Soul{Core: "theirs\n"}}, "codex", Options{})
	if merged.Soul.Core != "theirs\n" {
		t.Error("an empty source takes the imported core")
	}
}

func TestRenderWritesOnlyWhatIsNew(t *testing.T) {
	dir := t.TempDir()
	skill := filepath.Join(t.TempDir(), "tdd")
	if err := os.MkdirAll(skill, 0o755); err != nil {
		t.Fatal(err)
	}
	b := base()
	merged, _ := Merge(b, &model.Source{
		Skills: []model.Skill{{Name: "tdd", Description: "d", Dir: skill}},
		Soul:   model.Soul{Sections: []model.Section{{Name: "dotnet", Inclusion: model.InclusionFileMatch, Globs: []string{"**/*.cs"}, Body: "rules"}}},
	}, "kiro", Options{})
	p, err := Render(b, merged, dir)
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{}
	for _, o := range p.Ops {
		rel, _ := filepath.Rel(dir, o.Path)
		paths[filepath.ToSlash(rel)] = string(o.Content)
	}
	if len(paths) != 2 {
		t.Fatalf("only the new section and skill should be planned: %v", paths)
	}
	if !strings.HasPrefix(paths["soul/sections/dotnet.md"], "---\ninclusion: fileMatch\nglobs: ['**/*.cs']\n---\n") {
		t.Errorf("section:\n%s", paths["soul/sections/dotnet.md"])
	}
	if _, ok := paths["skills/tdd"]; !ok {
		t.Error("skill not planned")
	}
}

func skillAt(t *testing.T, dir string, extra func(string)) model.Skill {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: s\ndescription: d\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if extra != nil {
		extra(dir)
	}
	return model.Skill{Name: filepath.Base(dir), Description: "d", Dir: dir}
}

func TestUnsafeSkillsAreRefused(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".nexo-ai", "source")
	mk := func(rel string, f func(string)) model.Skill { return skillAt(t, filepath.Join(t.TempDir(), rel), f) }
	write := func(name string) func(string) {
		return func(d string) {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(d, name)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(d, name), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	cases := map[string]struct {
		skill model.Skill
		want  string
	}{
		"home itself":    {skillAt(t, home, nil), "contains " + realPath(t, home)},
		"symlink inside": {mk("linked", func(d string) { _ = os.Symlink("/etc", filepath.Join(d, "etc")) }), "contains the symlink"},
		"git checkout":   {mk("repo", write(".git/HEAD")), "repository checkout"},
		"env file":       {mk("secret", write(".env.local")), "secrets file"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			merged, warn := Merge(&model.Source{}, &model.Source{Skills: []model.Skill{c.skill}}, "claude", Options{Root: root, Home: home})
			if len(merged.Skills) != 0 || !strings.Contains(strings.Join(warn, "\n"), c.want) {
				t.Fatalf("want refusal containing %q, got skills=%v warn=%v", c.want, merged.Skills, warn)
			}
		})
	}
	safe := mk("plain", write("references/guide.md"))
	if merged, warn := Merge(&model.Source{}, &model.Source{Skills: []model.Skill{safe}}, "claude", Options{Root: root, Home: home}); len(merged.Skills) != 1 {
		t.Fatalf("a plain skill must be accepted: %v", warn)
	}
}

func TestContradictingPermissionAndReservedCore(t *testing.T) {
	b := &model.Source{Permissions: []model.Permission{{Capability: model.CapabilityRead, Match: []string{"**/.env"}, Effect: model.EffectDeny}}}
	merged, warn := Merge(b, &model.Source{
		Permissions: []model.Permission{{Capability: model.CapabilityRead, Match: []string{"**/.env", "docs/**"}, Effect: model.EffectAllow}},
		Soul:        model.Soul{Sections: []model.Section{{Name: "core", Inclusion: model.InclusionAlways, Body: "x"}}},
	}, "claude", Options{})
	if len(merged.Permissions) != 2 || strings.Join(merged.Permissions[1].Match, ",") != "docs/**" {
		t.Errorf("only the non-contradicting pattern is added: %+v", merged.Permissions)
	}
	all := strings.Join(warn, "\n")
	if !strings.Contains(all, `"**/.env" skipped: the source already says deny`) || !strings.Contains(all, `section "core" skipped: the name is reserved`) {
		t.Errorf("warnings:\n%s", all)
	}
	if len(merged.Soul.Sections) != 0 {
		t.Error("a section named core must not be imported")
	}
}

func TestSameHookFromSeveralAgentsBecomesOne(t *testing.T) {
	m := NewMerger(&model.Source{}, Options{})
	for _, agent := range []string{"claude", "kiro", "codex"} {
		m.Add(&model.Source{Hooks: []model.Hook{
			{Name: "guards", Event: model.EventPreToolUse, Matcher: model.MatcherWrite, Command: "node guards.js --harness " + agent},
		}}, agent)
	}
	m.Add(&model.Source{Hooks: []model.Hook{{Name: "notify", Event: model.EventStop, Command: "notify-claude.sh"}}}, "claude")
	hooks := m.Result().Hooks
	if len(hooks) != 2 {
		t.Fatalf("want one templated hook plus the single-agent one, got %+v", hooks)
	}
	if hooks[0].Command != "node guards.js --harness {{agent}}" || len(hooks[0].Agents) != 0 {
		t.Errorf("templated hook: %+v", hooks[0])
	}
	if hooks[1].Command != "notify-claude.sh" || strings.Join(hooks[1].Agents, ",") != "claude" {
		t.Errorf("a hook seen from one agent stays verbatim: %q", hooks[1].Command)
	}
}

func TestDifferentHooksFromTwoAgentsStayApart(t *testing.T) {
	m := NewMerger(&model.Source{}, Options{})
	m.Add(&model.Source{Hooks: []model.Hook{{Name: "a", Event: model.EventStop, Command: "claude-only.sh"}}}, "claude")
	m.Add(&model.Source{Hooks: []model.Hook{{Name: "b", Event: model.EventStop, Command: "other.sh"}}}, "kiro")
	if len(m.Result().Hooks) != 2 {
		t.Fatalf("%+v", m.Result().Hooks)
	}
}

func TestHomeTemplate(t *testing.T) {
	cases := map[string]string{
		`C:/Users/someone/node.exe "$HOME/g.js"`:   `{{home}}/node.exe "{{home}}/g.js"`,
		`C:\Users\someone\g.js`:                    `{{home}}\g.js`,
		`node "${HOME}/g.js" %USERPROFILE%\x`:      `node "{{home}}/g.js" {{home}}\x`,
		`node ~/g.js --cfg=~/c`:                    `node {{home}}/g.js --cfg={{home}}/c`,
		`node /home/dev/g.js`:                      `node {{home}}/g.js`,
		`echo $HOMEPATH and a~/b`:                  `echo $HOMEPATH and a~/b`,
		`${CLAUDE_CONFIG_DIR:-$HOME/.claude}/h.js`: `{{home}}/.claude/h.js`,
	}
	cases[`%CLAUDE_CONFIG_DIR%/mcp/sqlserver/server.js`] = `{{home}}/.claude/mcp/sqlserver/server.js`
	for in, want := range cases {
		if got := HomeTemplate(in, "/home/dev"); got != want {
			t.Errorf("HomeTemplate(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHomeTemplateSegmentsAndNesting(t *testing.T) {
	cases := map[string]string{
		`node "${CLAUDE_CONFIG_DIR:-${HOME}/.claude}/hooks/c.js"`: `node "{{home}}/.claude/hooks/c.js"`,
		`node "${HOME}2/other.js"`:                                `node "${HOME}2/other.js"`,
		`node /home/dev2/x.js /home/dev/y.js`:                     `node /home/dev2/x.js {{home}}/y.js`,
		`C:/Users/Public/tools/x.exe`:                             `C:/Users/Public/tools/x.exe`,
		`$CLAUDE_CONFIG_DIR/hooks/x.js`:                           `{{home}}/.claude/hooks/x.js`,
	}
	for in, want := range cases {
		if got := HomeTemplate(in, "/home/dev"); got != want {
			t.Errorf("HomeTemplate(%q) = %q, want %q", in, got, want)
		}
	}
}

// A Windows home matches in both slash forms, and other backslashes (escapes) stay.
func TestHomeTemplateKeepsEscapes(t *testing.T) {
	in := `node -e "run(\"a\\b\")" D:/work/me/x.js D:\work\me\y.js`
	want := `node -e "run(\"a\\b\")" {{home}}/x.js {{home}}\y.js`
	if got := HomeTemplate(in, `D:\work\me`); got != want {
		t.Errorf("HomeTemplate = %q, want %q", got, want)
	}
}

// realPath resolves symlinks and Windows short names (/var → /private/var on macOS,
// RUNNER~1 → runneradmin on Windows), as the importer does before comparing paths.
func realPath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
