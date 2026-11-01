package measure

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Madian2112/nexo-ai/internal/model"
	"github.com/Madian2112/nexo-ai/internal/source"
)

func TestEstimate(t *testing.T) {
	if Estimate("") != 0 || Estimate("abcd") != 2 || Estimate(strings.Repeat("a", 38)) != 10 {
		t.Fatal("unexpected estimate")
	}
	if Estimate("ñññ") != 1 {
		t.Error("estimate must count characters, not bytes")
	}
}

func load(t *testing.T, item string, r Report) Line {
	t.Helper()
	for _, l := range r.Lines {
		if strings.HasPrefix(l.Item, item) {
			return l
		}
	}
	t.Fatalf("no line %q in %+v", item, r.Lines)
	return Line{}
}

func TestPerAgentLoading(t *testing.T) {
	src, err := source.Load("../../source-example")
	if err != nil {
		t.Fatal(err)
	}
	claude, _ := For("claude", src)
	codex, _ := For("codex", src)
	kiro, _ := For("kiro", src)

	if load(t, "soul: fileMatch", claude).Load != OnDemand || load(t, "soul: fileMatch", codex).Load != NotInstalled {
		t.Error("fileMatch sections: on demand in Claude, not installed in Codex")
	}
	if load(t, "soul: intent", kiro).Load != NotInstalled {
		t.Error("intent sections are not installed until the router exists")
	}
	if load(t, "soul: core", claude).Tokens != Estimate(src.Soul.AlwaysSoul()) {
		t.Error("always soul tokens")
	}
	sum := 0
	for _, l := range claude.Lines {
		if l.Load == Always {
			sum += l.Tokens
		}
	}
	if sum != claude.Always || claude.Always == 0 {
		t.Errorf("always total %d != sum %d", claude.Always, sum)
	}
	if claude.Always <= codex.Always {
		t.Error("Claude also lists agent descriptions, so it should load more than Codex here")
	}
}

func TestKiroToolSearchNote(t *testing.T) {
	src := &model.Source{MCP: []model.MCPServer{{Name: "a", Command: "a"}}}
	r, _ := For("kiro", src)
	if !strings.Contains(strings.Join(r.Notes, "|"), "Tool Search off") {
		t.Errorf("notes: %v", r.Notes)
	}
	src.Settings.KiroToolSearch = true
	r, _ = For("kiro", src)
	if !strings.Contains(strings.Join(r.Notes, "|"), "deferred") {
		t.Errorf("notes: %v", r.Notes)
	}
}

func TestCodexSkillIndexBudget(t *testing.T) {
	dir := t.TempDir()
	src := &model.Source{}
	for i := 0; i < 50; i++ {
		d := filepath.Join(dir, "s"+string(rune('a'+i%26))+string(rune('a'+i/26)))
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		src.Skills = append(src.Skills, model.Skill{Name: filepath.Base(d), Description: strings.Repeat("d", 300), Dir: d})
	}
	codex, _ := For("codex", src)
	claude, _ := For("claude", src)
	if load(t, "skills: index", codex).Tokens >= load(t, "skills: index", claude).Tokens {
		t.Error("Codex index should be capped by its budget")
	}
	if !strings.Contains(strings.Join(codex.Notes, "|"), "truncated") {
		t.Errorf("notes: %v", codex.Notes)
	}
}
