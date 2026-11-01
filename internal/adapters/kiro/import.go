package kiro

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Madian2112/nexo-ai/internal/adapters/native"
	"github.com/Madian2112/nexo-ai/internal/model"
	"github.com/Madian2112/nexo-ai/internal/plan"
	"github.com/Madian2112/nexo-ai/internal/source"
)

var capabilityOf = map[string]string{
	"fs_read": model.CapabilityRead, "fs_write": model.CapabilityWrite,
	"shell": model.CapabilityShell, "mcp": model.CapabilityMCP,
}

// Import reads Kiro CLI's user-level config back into the canonical model.
func Import(env plan.Env) (*model.Source, []string, error) {
	src := &model.Source{}
	var warn []string
	dir := filepath.Join(env.Home, ".kiro")

	steering, _ := filepath.Glob(filepath.Join(dir, "steering", "*.md"))
	sort.Strings(steering)
	for _, p := range steering {
		var meta struct {
			Inclusion        string `yaml:"inclusion"`
			FileMatchPattern any    `yaml:"fileMatchPattern"`
			Description      string `yaml:"description"`
		}
		body, err := source.ReadMarkdown(p, &meta)
		if err != nil {
			warn = append(warn, fmt.Sprintf("steering %s skipped: %v", p, err))
			continue
		}
		name := native.SectionName(filepath.Base(p))
		if name == "core" {
			src.Soul.Core = body
			continue
		}
		sec := model.Section{Name: name, Description: meta.Description, Inclusion: model.InclusionAlways, Body: body}
		switch meta.Inclusion {
		case "", "always":
		case "fileMatch":
			sec.Inclusion = model.InclusionFileMatch
			sec.Globs = native.StringList(meta.FileMatchPattern)
			if len(sec.Globs) == 0 {
				warn = append(warn, fmt.Sprintf("steering %s skipped: fileMatch without fileMatchPattern", p))
				continue
			}
		default:
			// manual, auto and future modes load on request: the closest canonical mode is intent.
			sec.Inclusion = model.InclusionIntent
			sec.Keywords = []string{name}
			warn = append(warn, fmt.Sprintf("steering %q (inclusion %s) imported as intent with keyword %q; review its keywords", name, meta.Inclusion, name))
		}
		src.Soul.Sections = append(src.Soul.Sections, sec)
	}

	skills, w := source.ScanSkills(filepath.Join(dir, "skills"))
	src.Skills, warn = skills, append(warn, w...)

	cfg, err := env.JSONObject(filepath.Join(dir, "settings", "mcp.json"))
	if err != nil {
		return nil, nil, err
	}
	src.MCP, w = native.MCPServers(cfg["mcpServers"], "headers")
	warn = append(warn, w...)
	known := map[string]bool{}
	for _, s := range src.MCP {
		known[s.Name] = true
	}

	agents, _ := filepath.Glob(filepath.Join(dir, "agents", "*.json"))
	sort.Strings(agents)
	for _, p := range agents {
		a, w, err := importAgent(env, p, known)
		warn = append(warn, w...)
		if err != nil {
			warn = append(warn, fmt.Sprintf("agent %s skipped: %v", p, err))
			continue
		}
		src.Agents = append(src.Agents, a)
	}

	hookFiles, _ := filepath.Glob(filepath.Join(dir, "hooks", "*.json"))
	sort.Strings(hookFiles)
	for _, p := range hookFiles {
		obj, err := env.JSONObject(p)
		if err != nil {
			warn = append(warn, fmt.Sprintf("hooks %s skipped: %v", p, err))
			continue
		}
		hs, w := importHooks(obj["hooks"], filepath.Base(p))
		src.Hooks = append(src.Hooks, hs...)
		warn = append(warn, w...)
	}

	perms, err := env.ReadOptional(filepath.Join(dir, "settings", "permissions.yaml"))
	if err != nil {
		return nil, nil, err
	}
	if len(perms) > 0 {
		var f struct {
			Rules []struct {
				Capability string   `yaml:"capability"`
				Match      []string `yaml:"match"`
				Effect     string   `yaml:"effect"`
				Exclude    []string `yaml:"exclude"`
			} `yaml:"rules"`
		}
		if err := yaml.Unmarshal(perms, &f); err != nil {
			return nil, nil, fmt.Errorf("permissions.yaml: %w", err)
		}
		for i, r := range f.Rules {
			c, ok := capabilityOf[r.Capability]
			if !ok || len(r.Match) == 0 || (r.Effect != model.EffectAllow && r.Effect != model.EffectAsk && r.Effect != model.EffectDeny) {
				warn = append(warn, fmt.Sprintf("permission rule #%d (%s %s) skipped: no canonical equivalent", i+1, r.Capability, r.Effect))
				continue
			}
			if len(r.Exclude) > 0 {
				warn = append(warn, fmt.Sprintf("permission rule #%d: its exclude list is not imported", i+1))
			}
			src.Permissions = append(src.Permissions, model.Permission{Capability: c, Match: r.Match, Effect: r.Effect})
		}
	}
	return src, warn, nil
}

func importAgent(env plan.Env, path string, knownMCP map[string]bool) (model.Agent, []string, error) {
	b, err := env.ReadFile(path)
	if err != nil {
		return model.Agent{}, nil, err
	}
	var cfg struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Prompt      string   `json:"prompt"`
		Model       string   `json:"model"`
		Tools       []string `json:"tools"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return model.Agent{}, nil, err
	}
	if cfg.Name == "" {
		cfg.Name = strings.TrimSuffix(filepath.Base(path), ".json")
	}
	if cfg.Description == "" {
		return model.Agent{}, nil, fmt.Errorf("no description")
	}
	var warn []string
	prompt := cfg.Prompt
	if ref, ok := strings.CutPrefix(prompt, "file://"); ok {
		p := ref
		if rest, home := strings.CutPrefix(ref, "~/"); home {
			p = filepath.Join(env.Home, rest)
		} else if !filepath.IsAbs(ref) {
			p = filepath.Join(filepath.Dir(path), ref)
		}
		// Only read prompts inside ~/.kiro: an agent file must not pull arbitrary files into the source.
		// Symlinks are resolved first, so a link inside ~/.kiro cannot point the read elsewhere.
		kiroDir := filepath.Join(env.Home, ".kiro")
		realDir, errDir := filepath.EvalSymlinks(kiroDir)
		realPrompt, errPrompt := filepath.EvalSymlinks(p)
		if errDir != nil || errPrompt != nil {
			return model.Agent{}, nil, fmt.Errorf("prompt %s: cannot resolve it", p)
		}
		if rel, err := filepath.Rel(realDir, realPrompt); err != nil || strings.HasPrefix(rel, "..") {
			return model.Agent{}, nil, fmt.Errorf("prompt %s is outside %s", p, kiroDir)
		}
		body, err := env.ReadFile(p)
		if err != nil {
			return model.Agent{}, nil, fmt.Errorf("prompt %s: %w", p, err)
		}
		prompt = string(body)
	}
	a := model.Agent{Name: cfg.Name, Description: cfg.Description, Model: cfg.Model, Prompt: prompt}
	for _, t := range cfg.Tools {
		server, ok := strings.CutPrefix(t, "@")
		if !ok || server == "builtin" || strings.Contains(server, "/") {
			continue
		}
		if knownMCP[server] {
			a.MCP = append(a.MCP, server)
		} else {
			warn = append(warn, fmt.Sprintf("agent %q: tool @%s is not a defined MCP server, dropped", cfg.Name, server))
		}
	}
	return a, warn, nil
}

func importHooks(v any, file string) ([]model.Hook, []string) {
	list, _ := v.([]any)
	var out []model.Hook
	var warn []string
	for i, h := range list {
		hook, _ := h.(map[string]any)
		name, _ := hook["name"].(string)
		if name == "" {
			name = fmt.Sprintf("kiro-%s-%d", strings.TrimSuffix(file, ".json"), i+1)
		}
		trigger, _ := hook["trigger"].(string)
		matcher, _ := hook["matcher"].(string)
		action, _ := hook["action"].(map[string]any)
		kind, _ := action["type"].(string)
		command, _ := action["command"].(string)
		switch {
		case !native.CanonicalEvents[trigger]:
			warn = append(warn, fmt.Sprintf("hook %q skipped: trigger %s has no canonical event", name, trigger))
			continue
		case matcher != "" && matcher != "*" && matcher != model.MatcherWrite && matcher != model.MatcherShell && matcher != model.MatcherRead:
			warn = append(warn, fmt.Sprintf("hook %q skipped: matcher %q has no canonical kind", name, matcher))
			continue
		case kind != "command" || command == "":
			warn = append(warn, fmt.Sprintf("hook %q skipped: only command hooks are portable", name))
			continue
		}
		if matcher == "*" {
			matcher = model.MatcherAny
		}
		timeout, _ := hook["timeout"].(float64)
		description, _ := hook["description"].(string)
		out = append(out, model.Hook{Name: name, Description: description, Event: trigger, Matcher: matcher, Command: command, Timeout: int(timeout)})
	}
	return out, warn
}
