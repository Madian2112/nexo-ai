package codex

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/Madian2112/nexo-ai/internal/adapters/native"
	"github.com/Madian2112/nexo-ai/internal/model"
	"github.com/Madian2112/nexo-ai/internal/plan"
	"github.com/Madian2112/nexo-ai/internal/source"
)

// Import reads Codex CLI's user-level config back into the canonical model.
// Codex has no per-path permission rules, so none are imported.
func Import(env plan.Env) (*model.Source, []string, error) {
	src := &model.Source{}
	var warn []string
	dir := filepath.Join(env.Home, ".codex")

	core, err := env.ReadOptional(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		return nil, nil, err
	}
	src.Soul.Core = string(core)

	skills, w := source.ScanSkills(filepath.Join(env.Home, ".agents", "skills"))
	src.Skills, warn = skills, append(warn, w...)

	agents, _ := filepath.Glob(filepath.Join(dir, "agents", "*.toml"))
	sort.Strings(agents)
	for _, p := range agents {
		b, err := env.ReadFile(p)
		if err != nil {
			return nil, nil, err
		}
		var cfg struct {
			Name                  string `toml:"name"`
			Description           string `toml:"description"`
			DeveloperInstructions string `toml:"developer_instructions"`
			Model                 string `toml:"model"`
		}
		if err := toml.Unmarshal(b, &cfg); err != nil || cfg.Description == "" {
			warn = append(warn, fmt.Sprintf("agent %s skipped: needs name, description and developer_instructions (%v)", p, err))
			continue
		}
		if cfg.Name == "" {
			cfg.Name = strings.TrimSuffix(filepath.Base(p), ".toml")
		}
		src.Agents = append(src.Agents, model.Agent{Name: cfg.Name, Description: cfg.Description, Model: cfg.Model, Prompt: cfg.DeveloperInstructions})
	}

	hooks, err := env.JSONObject(filepath.Join(dir, "hooks.json"))
	if err != nil {
		return nil, nil, err
	}
	src.Hooks, w = importHooks(hooks["hooks"])
	warn = append(warn, w...)

	b, err := env.ReadOptional(filepath.Join(dir, "config.toml"))
	if err != nil {
		return nil, nil, err
	}
	cfg := map[string]any{}
	if err := toml.Unmarshal(b, &cfg); err != nil {
		return nil, nil, fmt.Errorf("config.toml: %w", err)
	}
	if _, inline := cfg["hooks"]; inline {
		warn = append(warn, "inline [hooks] in config.toml are not imported; only hooks.json is")
	}
	src.MCP, w = native.MCPServers(cfg["mcp_servers"], "http_headers")
	// env_vars passes variables through from Codex's environment: canonical {{env:NAME}}.
	servers, _ := cfg["mcp_servers"].(map[string]any)
	for i, s := range src.MCP {
		entry, _ := servers[s.Name].(map[string]any)
		for _, name := range native.StringList(entry["env_vars"]) {
			if src.MCP[i].Env == nil {
				src.MCP[i].Env = map[string]string{}
			}
			src.MCP[i].Env[name] = "{{env:" + name + "}}"
		}
	}
	warn = append(warn, w...)
	return src, warn, nil
}

var toolKind = map[string]string{
	"apply_patch": model.MatcherWrite, "Edit": model.MatcherWrite, "Write": model.MatcherWrite,
	"Bash": model.MatcherShell,
}

func importHooks(v any) ([]model.Hook, []string) {
	events, _ := v.(map[string]any)
	var out []model.Hook
	var warn []string
	for _, event := range native.SortedKeys(events) {
		groups, _ := events[event].([]any)
		if !native.CanonicalEvents[event] {
			warn = append(warn, fmt.Sprintf("%d hook groups on %s skipped: no canonical event", len(groups), event))
			continue
		}
		for _, g := range groups {
			group, _ := g.(map[string]any)
			m, _ := group["matcher"].(string)
			kinds, ok := native.MatcherKinds(m, toolKind)
			handlers, _ := group["hooks"].([]any)
			if !ok {
				warn = append(warn, fmt.Sprintf("%d %s hooks with matcher %q skipped: no canonical kind", len(handlers), event, m))
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
					out = append(out, model.Hook{
						Name:    fmt.Sprintf("codex-%s-%d", strings.ToLower(event), len(out)+1),
						Event:   event,
						Matcher: kind,
						Command: command,
						Timeout: int(timeout),
					})
				}
			}
		}
	}
	return out, warn
}
