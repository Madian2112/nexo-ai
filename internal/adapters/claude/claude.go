// Package claude compiles a model.Source into Claude Code's user-level config (~/.claude).
package claude

import (
	"path/filepath"
	"strings"

	"github.com/Madian2112/nexo-ai/internal/model"
	"github.com/Madian2112/nexo-ai/internal/plan"
)

const Name = "claude"

var matchers = map[string]string{
	model.MatcherWrite: "Edit|MultiEdit|Write|NotebookEdit",
	model.MatcherShell: "Bash|PowerShell",
	model.MatcherRead:  "Read|Grep|Glob",
}

var permissionTool = map[string]string{
	model.CapabilityRead:  "Read",
	model.CapabilityWrite: "Edit",
	model.CapabilityShell: "Bash",
}

func Plan(src *model.Source, env plan.Env) (plan.Plan, error) {
	p := plan.Plan{Agent: Name}
	dir := filepath.Join(env.Home, ".claude")

	if soul := src.Soul.AlwaysSoul(); soul != "" {
		p.File(filepath.Join(dir, "CLAUDE.md"), []byte(soul))
	}
	for _, sec := range src.Soul.Sections {
		switch sec.Inclusion {
		case model.InclusionFileMatch:
			p.File(filepath.Join(dir, "rules", "nexo-"+sec.Name+".md"), []byte(rule(sec)))
		case model.InclusionIntent:
			p.Warn("section %q (intent) is not installed yet: intent routing arrives with the router", sec.Name)
		}
	}
	for _, s := range src.Skills {
		p.Link(filepath.Join(dir, "skills", s.Name), s.Dir)
	}
	for _, a := range src.Agents {
		a.Model = p.Model(src.Settings, a)
		p.File(filepath.Join(dir, "agents", a.Name+".md"), []byte(agent(a)))
	}

	settingsPath := filepath.Join(dir, "settings.json")
	if env.Need(settingsPath, len(src.Hooks) > 0 || len(src.Permissions) > 0) {
		settings, err := env.JSONObject(settingsPath)
		if err != nil {
			return p, err
		}
		shared := env.Shared(settingsPath)
		var hookValue any
		if hs := hooks(src.Hooks, env.Home, src.Settings.HookAgentFor(Name)); len(hs) > 0 {
			hookValue = hs
		}
		shared.Set(settings, "hooks", hookValue)
		setPermissions(settings, shared, src.Permissions)
		if shared.Touched() {
			p.Merged(settingsPath, plan.JSON(settings), shared.Keys)
		}
	}

	statePath := filepath.Join(env.Home, ".claude.json")
	if env.Need(statePath, len(src.MCP) > 0) {
		state, err := env.JSONObject(statePath)
		if err != nil {
			return p, err
		}
		shared := env.Shared(statePath)
		var servers any
		if len(src.MCP) > 0 {
			servers = mcpServers(src.MCP, env.Home)
		}
		shared.Set(state, "mcpServers", servers)
		if shared.Touched() {
			p.Merged(statePath, plan.JSON(state), shared.Keys)
		}
	}
	return p, nil
}

func rule(sec model.Section) string {
	var b strings.Builder
	b.WriteString("---\npaths:\n")
	for _, g := range sec.Globs {
		b.WriteString("  - \"" + g + "\"\n")
	}
	b.WriteString("---\n\n")
	b.WriteString(strings.Trim(sec.Body, "\n") + "\n")
	return b.String()
}

func agent(a model.Agent) string {
	var b strings.Builder
	b.WriteString("---\nname: " + a.Name + "\n")
	b.WriteString("description: " + yamlString(a.Description) + "\n")
	if a.Model != "" {
		b.WriteString("model: " + a.Model + "\n")
	}
	b.WriteString("---\n\n")
	b.WriteString(strings.Trim(a.Prompt, "\n") + "\n")
	return b.String()
}

func yamlString(s string) string {
	return "\"" + strings.ReplaceAll(strings.ReplaceAll(s, "\\", "\\\\"), "\"", "\\\"") + "\""
}

func hooks(hs []model.Hook, home, agentName string) map[string]any {
	out := map[string]any{}
	for _, h := range hs {
		if !h.For(Name) {
			continue
		}
		handler := map[string]any{"type": "command", "command": plan.HookCommand(h.Command, agentName, home)}
		if h.Timeout > 0 {
			handler["timeout"] = h.Timeout
		}
		group := map[string]any{"hooks": []any{handler}}
		if m := matchers[h.Matcher]; m != "" {
			group["matcher"] = m
		}
		list, _ := out[h.Event].([]any)
		out[h.Event] = append(list, group)
	}
	return out
}

// permissionsKey is the owned key recorded in state: allow/ask/deny inside "permissions".
// Every other permissions key (defaultMode, additionalDirectories, ...) stays the user's.
const permissionsKey = "permissions.rules"

var effects = []string{model.EffectAllow, model.EffectAsk, model.EffectDeny}

func setPermissions(settings map[string]any, shared *plan.Shared, rules []model.Permission) {
	perms, _ := settings["permissions"].(map[string]any)
	if len(rules) == 0 {
		if perms == nil || !shared.WasOwned(permissionsKey) {
			return
		}
		for _, e := range effects {
			delete(perms, e)
		}
		if len(perms) == 0 {
			delete(settings, "permissions")
		}
		return
	}
	out := map[string]any{}
	for k, v := range perms {
		out[k] = v
	}
	lists := map[string][]string{model.EffectAllow: {}, model.EffectAsk: {}, model.EffectDeny: {}}
	for _, r := range rules {
		for _, pattern := range r.Match {
			lists[r.Effect] = append(lists[r.Effect], permissionRule(r.Capability, pattern))
		}
	}
	for effect, l := range lists {
		out[effect] = l
	}
	settings["permissions"] = out
	shared.Own(permissionsKey)
}

func permissionRule(capability, pattern string) string {
	if capability == model.CapabilityMCP {
		if strings.HasPrefix(pattern, "mcp__") {
			return pattern
		}
		return "mcp__" + pattern
	}
	return permissionTool[capability] + "(" + pattern + ")"
}

func mcpServers(servers []model.MCPServer, home string) map[string]any {
	out := map[string]any{}
	for _, s := range servers {
		s = plan.ResolveServer(s, home, "${%s}")
		if s.Remote() {
			entry := map[string]any{"type": "http", "url": s.URL}
			if len(s.Headers) > 0 {
				entry["headers"] = s.Headers
			}
			out[s.Name] = entry
			continue
		}
		entry := map[string]any{"type": "stdio", "command": s.Command, "args": nonNil(s.Args)}
		if len(s.Env) > 0 {
			entry["env"] = s.Env
		}
		out[s.Name] = entry
	}
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
