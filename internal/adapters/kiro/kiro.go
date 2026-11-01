// Package kiro compiles a model.Source into Kiro CLI v3's user-level config (~/.kiro).
package kiro

import (
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Madian2112/nexo-ai/internal/model"
	"github.com/Madian2112/nexo-ai/internal/plan"
)

const Name = "kiro"

var supportedEvents = map[string]bool{
	model.EventSessionStart:     true,
	model.EventUserPromptSubmit: true,
	model.EventPreToolUse:       true,
	model.EventPostToolUse:      true,
	model.EventStop:             true,
}

var capabilities = map[string]string{
	model.CapabilityRead:  "fs_read",
	model.CapabilityWrite: "fs_write",
	model.CapabilityShell: "shell",
	model.CapabilityMCP:   "mcp",
}

func Plan(src *model.Source, env plan.Env) (plan.Plan, error) {
	p := plan.Plan{Agent: Name}
	dir := filepath.Join(env.Home, ".kiro")

	if src.Soul.Core != "" {
		p.File(filepath.Join(dir, "steering", "nexo-core.md"), []byte(steering("inclusion: always\n", src.Soul.Core)))
	}
	for _, sec := range src.Soul.Sections {
		path := filepath.Join(dir, "steering", "nexo-"+sec.Name+".md")
		switch sec.Inclusion {
		case model.InclusionAlways:
			p.File(path, []byte(steering("inclusion: always\n", sec.Body)))
		case model.InclusionFileMatch:
			p.File(path, []byte(steering("inclusion: fileMatch\nfileMatchPattern: "+inlineList(sec.Globs)+"\n", sec.Body)))
		case model.InclusionIntent:
			p.Warn("section %q (intent) is not installed yet: intent routing arrives with the router", sec.Name)
		}
	}
	for _, s := range src.Skills {
		p.Link(filepath.Join(dir, "skills", s.Name), s.Dir)
	}
	for _, a := range src.Agents {
		a.Model = p.Model(src.Settings, a)
		p.File(filepath.Join(dir, "prompts", a.Name+".md"), []byte(strings.Trim(a.Prompt, "\n")+"\n"))
		p.File(filepath.Join(dir, "agents", a.Name+".json"), plan.JSON(agent(a)))
	}

	if hs := hooks(src.Hooks, env.Home, src.Settings.HookAgentFor(Name), &p); len(hs) > 0 {
		p.File(filepath.Join(dir, "hooks", "nexo.json"), plan.JSON(map[string]any{"version": "v1", "hooks": hs}))
	}

	if len(src.Permissions) > 0 {
		b, err := yaml.Marshal(map[string]any{"rules": rules(src.Permissions)})
		if err != nil {
			return p, err
		}
		p.File(filepath.Join(dir, "settings", "permissions.yaml"), b)
	}

	mcpPath := filepath.Join(dir, "settings", "mcp.json")
	if env.Need(mcpPath, len(src.MCP) > 0) {
		cfg, err := env.JSONObject(mcpPath)
		if err != nil {
			return p, err
		}
		shared := env.Shared(mcpPath)
		var servers any
		if len(src.MCP) > 0 {
			servers = mcpServers(src.MCP, env.Home)
		}
		shared.Set(cfg, "mcpServers", servers)
		if shared.Touched() {
			p.Merged(mcpPath, plan.JSON(cfg), shared.Keys)
		}
	}

	// Tool Search is off by default in Kiro; it defers MCP tool schemas until needed.
	toolSearch := len(src.MCP) > 0 && src.Settings.KiroToolSearch
	cliPath := filepath.Join(dir, "settings", "cli.json")
	if env.Need(cliPath, toolSearch) {
		cli, err := env.JSONObject(cliPath)
		if err != nil {
			return p, err
		}
		shared := env.Shared(cliPath)
		var enabled any
		if toolSearch {
			enabled = true
		}
		shared.Set(cli, "toolSearch.enabled", enabled)
		if shared.Touched() {
			p.Merged(cliPath, plan.JSON(cli), shared.Keys)
		}
	}
	return p, nil
}

func steering(frontmatter, body string) string {
	return "---\n" + frontmatter + "---\n\n" + strings.Trim(body, "\n") + "\n"
}

func inlineList(items []string) string {
	quoted := make([]string, len(items))
	for i, it := range items {
		quoted[i] = "\"" + it + "\""
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

func agent(a model.Agent) map[string]any {
	tools := []string{"@builtin"}
	for _, m := range a.MCP {
		tools = append(tools, "@"+m)
	}
	out := map[string]any{
		"name":           a.Name,
		"description":    a.Description,
		"prompt":         "file://../prompts/" + a.Name + ".md",
		"tools":          tools,
		"includeMcpJson": true,
		"resources":      []string{"skill://~/.kiro/skills/**/SKILL.md"},
	}
	if a.Model != "" {
		out["model"] = a.Model
	}
	return out
}

func hooks(hs []model.Hook, home, agentName string, p *plan.Plan) []any {
	out := []any{}
	for _, h := range hs {
		if !h.For(Name) {
			continue
		}
		if !supportedEvents[h.Event] {
			p.Warn("hook %q skipped: event %s is not mapped for Kiro", h.Name, h.Event)
			continue
		}
		entry := map[string]any{
			"name":    h.Name,
			"trigger": h.Event,
			"action":  map[string]any{"type": "command", "command": plan.HookCommand(h.Command, agentName, home)},
		}
		if h.Description != "" {
			entry["description"] = h.Description
		}
		if h.Matcher != model.MatcherAny {
			entry["matcher"] = h.Matcher
		}
		if h.Timeout > 0 {
			entry["timeout"] = h.Timeout
		}
		out = append(out, entry)
	}
	return out
}

func rules(ps []model.Permission) []map[string]any {
	out := make([]map[string]any, 0, len(ps))
	for _, r := range ps {
		out = append(out, map[string]any{"capability": capabilities[r.Capability], "match": r.Match, "effect": r.Effect})
	}
	return out
}

func mcpServers(servers []model.MCPServer, home string) map[string]any {
	out := map[string]any{}
	for _, s := range servers {
		s = plan.ResolveServer(s, home, "${%s}")
		if s.Remote() {
			entry := map[string]any{"url": s.URL}
			if len(s.Headers) > 0 {
				entry["headers"] = s.Headers
			}
			out[s.Name] = entry
			continue
		}
		args := s.Args
		if args == nil {
			args = []string{}
		}
		entry := map[string]any{"command": s.Command, "args": args}
		if len(s.Env) > 0 {
			entry["env"] = s.Env
		}
		out[s.Name] = entry
	}
	return out
}
