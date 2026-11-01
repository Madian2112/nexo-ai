// Package measure estimates, offline, how many tokens each agent loads from the
// source at session start versus on demand. Anthropic does not publish its tokenizer,
// so counts use a characters-per-token ratio: compare the numbers, do not bill on them.
package measure

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/Madian2112/nexo-ai/internal/model"
)

const charsPerToken = 3.8

// codexSkillIndexChars is Codex's skill-list budget when the context window is unknown.
const codexSkillIndexChars = 8000

const (
	Always       = "always"
	OnDemand     = "on demand"
	NotInstalled = "not installed"
)

type Line struct {
	Item   string `json:"item"`
	Load   string `json:"load"`
	Tokens int    `json:"tokens"`
}

type Report struct {
	Agent    string   `json:"agent"`
	Always   int      `json:"always"`
	OnDemand int      `json:"onDemand"`
	Lines    []Line   `json:"lines"`
	Notes    []string `json:"notes"`
}

func Estimate(s string) int {
	return int(math.Ceil(float64(utf8.RuneCountInString(s)) / charsPerToken))
}

func (r *Report) add(item, load string, tokens int) {
	r.Lines = append(r.Lines, Line{Item: item, Load: load, Tokens: tokens})
	switch load {
	case Always:
		r.Always += tokens
	case OnDemand:
		r.OnDemand += tokens
	}
}

func For(agent string, src *model.Source) (Report, error) {
	r := Report{Agent: agent, Lines: []Line{}, Notes: []string{}}

	always := 0
	fileMatch, intent := "", ""
	for _, s := range src.Soul.Sections {
		switch s.Inclusion {
		case model.InclusionAlways:
			always++
		case model.InclusionFileMatch:
			fileMatch += s.Body
		case model.InclusionIntent:
			intent += s.Body
		}
	}
	r.add(fmt.Sprintf("soul: core + %d always sections", always), Always, Estimate(src.Soul.AlwaysSoul()))
	if fileMatch != "" {
		load := OnDemand
		if agent == "codex" || agent == "opencode" {
			load = NotInstalled
		}
		r.add("soul: fileMatch sections", load, Estimate(fileMatch))
	}
	if intent != "" {
		r.add("soul: intent sections (router pending)", NotInstalled, Estimate(intent))
	}

	index, bodies := "", 0
	for _, s := range src.Skills {
		index += s.Name + ": " + s.Description + "\n"
		b, err := os.ReadFile(filepath.Join(s.Dir, "SKILL.md"))
		if err != nil {
			return r, err
		}
		bodies += Estimate(string(b))
	}
	if len(src.Skills) > 0 {
		tokens := Estimate(index)
		if agent == "codex" && utf8.RuneCountInString(index) > codexSkillIndexChars {
			tokens = int(math.Ceil(codexSkillIndexChars / charsPerToken))
			r.Notes = append(r.Notes, "the Codex skill index exceeds its default budget and will be truncated")
		}
		r.add(fmt.Sprintf("skills: index of %d", len(src.Skills)), Always, tokens)
		r.add("skills: bodies", OnDemand, bodies)
	}

	if len(src.Agents) > 0 {
		if agent == "claude" {
			desc := ""
			for _, a := range src.Agents {
				desc += a.Name + ": " + a.Description + "\n"
			}
			r.add(fmt.Sprintf("agents: index of %d", len(src.Agents)), Always, Estimate(desc))
		}
		prompts := 0
		for _, a := range src.Agents {
			prompts += Estimate(a.Prompt)
		}
		r.add("agents: prompts (only when that agent runs)", OnDemand, prompts)
	}

	if len(src.MCP) > 0 {
		// OpenCode has no tool search: every MCP tool schema is sent from the start.
		deferred := agent == "claude" || agent == "codex" || (agent == "kiro" && src.Settings.KiroToolSearch)
		switch {
		case deferred:
			r.Notes = append(r.Notes, fmt.Sprintf("%d MCP servers: tool schemas deferred behind native tool search", len(src.MCP)))
		case agent == "kiro":
			r.Notes = append(r.Notes, fmt.Sprintf("%d MCP servers: tool schemas load at start (Tool Search off); their size is not measurable offline", len(src.MCP)))
		default:
			r.Notes = append(r.Notes, fmt.Sprintf("%d MCP servers: tool schemas load at start (no tool search); their size is not measurable offline", len(src.MCP)))
		}
	}
	r.Notes = append(r.Notes, "excludes the agent's own system prompt and built-in tools")
	return r, nil
}
