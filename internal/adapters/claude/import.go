package claude

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Madian2112/nexo-ai/internal/adapters/native"
	"github.com/Madian2112/nexo-ai/internal/model"
	"github.com/Madian2112/nexo-ai/internal/plan"
	"github.com/Madian2112/nexo-ai/internal/source"
)

// Import reads Claude Code's user-level config back into the canonical model.
// Anything without a canonical equivalent is reported as a warning, never dropped silently.
func Import(env plan.Env) (*model.Source, []string, error) {
	src := &model.Source{}
	var warn []string
	dir := filepath.Join(env.Home, ".claude")

	core, err := env.ReadOptional(filepath.Join(dir, "CLAUDE.md"))
	if err != nil {
		return nil, nil, err
	}
	src.Soul.Core = string(core)

	rules, _ := filepath.Glob(filepath.Join(dir, "rules", "*.md"))
	sort.Strings(rules)
	for _, p := range rules {
		var meta struct {
			Paths any `yaml:"paths"`
		}
		body, err := source.ReadMarkdown(p, &meta)
		if err != nil {
			warn = append(warn, fmt.Sprintf("rule %s skipped: %v", p, err))
			continue
		}
		sec := model.Section{Name: native.SectionName(filepath.Base(p)), Inclusion: model.InclusionAlways, Body: body}
		if globs := native.StringList(meta.Paths); len(globs) > 0 {
			sec.Inclusion = model.InclusionFileMatch
			sec.Globs = globs
		}
		src.Soul.Sections = append(src.Soul.Sections, sec)
	}

	skills, w := source.ScanSkills(filepath.Join(dir, "skills"))
	src.Skills, warn = skills, append(warn, w...)

	agents, _ := filepath.Glob(filepath.Join(dir, "agents", "*.md"))
	sort.Strings(agents)
	for _, p := range agents {
		var meta struct {
			Name        string `yaml:"name"`
			Description string `yaml:"description"`
			Model       string `yaml:"model"`
			Tools       any    `yaml:"tools"`
		}
		body, err := source.ReadMarkdown(p, &meta)
		if err != nil || meta.Description == "" {
			warn = append(warn, fmt.Sprintf("agent %s skipped: needs frontmatter with a description (%v)", p, err))
			continue
		}
		if meta.Name == "" {
			meta.Name = strings.TrimSuffix(filepath.Base(p), ".md")
		}
		if meta.Tools != nil {
			warn = append(warn, fmt.Sprintf("agent %q: its tools list is not imported", meta.Name))
		}
		src.Agents = append(src.Agents, model.Agent{Name: meta.Name, Description: meta.Description, Model: meta.Model, Prompt: body})
	}

	settings, err := env.JSONObject(filepath.Join(dir, "settings.json"))
	if err != nil {
		return nil, nil, err
	}
	src.Hooks, w = importHooks(settings["hooks"])
	warn = append(warn, w...)
	src.Permissions, w = importPermissions(settings["permissions"])
	warn = append(warn, w...)

	state, err := env.JSONObject(filepath.Join(env.Home, ".claude.json"))
	if err != nil {
		return nil, nil, err
	}
	src.MCP, w = native.MCPServers(state["mcpServers"], "headers")
	warn = append(warn, w...)
	return src, warn, nil
}

var toolKind = map[string]string{
	"Edit": model.MatcherWrite, "MultiEdit": model.MatcherWrite, "Write": model.MatcherWrite, "NotebookEdit": model.MatcherWrite,
	"Bash": model.MatcherShell, "PowerShell": model.MatcherShell,
	"Read": model.MatcherRead, "Grep": model.MatcherRead, "Glob": model.MatcherRead,
}

func importHooks(v any) ([]model.Hook, []string) {
	events, _ := v.(map[string]any)
	var hooks []model.Hook
	var warn []string
	for _, event := range native.SortedKeys(events) {
		groups, _ := events[event].([]any)
		if !native.CanonicalEvents[event] {
			warn = append(warn, fmt.Sprintf("%d hook groups on %s skipped: no canonical event", len(groups), event))
			continue
		}
		for _, g := range groups {
			group, _ := g.(map[string]any)
			nativeMatcher, _ := group["matcher"].(string)
			kinds, ok := native.MatcherKinds(nativeMatcher, toolKind)
			handlers, _ := group["hooks"].([]any)
			if !ok {
				warn = append(warn, fmt.Sprintf("%d %s hooks with matcher %q skipped: it names tools without a canonical kind", len(handlers), event, nativeMatcher))
				continue
			}
			for _, h := range handlers {
				handler, _ := h.(map[string]any)
				command, _ := handler["command"].(string)
				if t, _ := handler["type"].(string); t != "command" || command == "" {
					warn = append(warn, fmt.Sprintf("%s hook of type %q skipped: only command hooks are portable", event, t))
					continue
				}
				timeout, _ := handler["timeout"].(float64)
				for _, kind := range kinds {
					hooks = append(hooks, model.Hook{
						Name:    fmt.Sprintf("claude-%s-%d", strings.ToLower(event), len(hooks)+1),
						Event:   event,
						Matcher: kind,
						Command: command,
						Timeout: int(timeout),
					})
				}
			}
		}
	}
	return hooks, warn
}

var capabilityOf = map[string]string{
	"Read": model.CapabilityRead, "Edit": model.CapabilityWrite, "Write": model.CapabilityWrite,
	"Bash": model.CapabilityShell,
}

func importPermissions(v any) ([]model.Permission, []string) {
	perms, _ := v.(map[string]any)
	var out []model.Permission
	var warn []string
	for _, effect := range effects {
		entries, _ := perms[effect].([]any)
		byCapability := map[string][]string{}
		for _, e := range entries {
			rule, _ := e.(string)
			capability, pattern, ok := parseRule(rule)
			if !ok {
				warn = append(warn, fmt.Sprintf("permission %s %q skipped: no canonical capability", effect, rule))
				continue
			}
			byCapability[capability] = append(byCapability[capability], pattern)
		}
		for _, c := range []string{model.CapabilityRead, model.CapabilityWrite, model.CapabilityShell, model.CapabilityMCP} {
			if len(byCapability[c]) > 0 {
				out = append(out, model.Permission{Capability: c, Match: byCapability[c], Effect: effect})
			}
		}
	}
	return out, warn
}

// parseRule splits "Tool(pattern)" or a bare tool name.
func parseRule(rule string) (capability, pattern string, ok bool) {
	if strings.HasPrefix(rule, "mcp__") {
		return model.CapabilityMCP, strings.TrimPrefix(rule, "mcp__"), true
	}
	tool, pattern := rule, "*"
	if i := strings.Index(rule, "("); i > 0 && strings.HasSuffix(rule, ")") {
		tool, pattern = rule[:i], rule[i+1:len(rule)-1]
	}
	capability, ok = capabilityOf[tool]
	return capability, pattern, ok
}
