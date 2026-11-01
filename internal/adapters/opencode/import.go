package opencode

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Madian2112/nexo-ai/internal/adapters/native"
	"github.com/Madian2112/nexo-ai/internal/hookrun"
	"github.com/Madian2112/nexo-ai/internal/model"
	"github.com/Madian2112/nexo-ai/internal/plan"
	"github.com/Madian2112/nexo-ai/internal/source"
)

var capabilityOf = map[string]string{"read": model.CapabilityRead, "edit": model.CapabilityWrite, "bash": model.CapabilityShell}

// Import reads OpenCode's user-level config back into the canonical model. Hooks come
// back only from the plugin nexo-ai generated; other plugins are code and are reported.
func Import(env plan.Env) (*model.Source, []string, error) {
	src := &model.Source{}
	var warn []string
	dir := Dir(env)

	core, err := env.ReadOptional(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		return nil, nil, err
	}
	src.Soul.Core = string(core)

	skills, w := source.ScanSkills(filepath.Join(dir, "skills"))
	src.Skills, warn = skills, append(warn, w...)

	agents, _ := filepath.Glob(filepath.Join(dir, "agents", "*.md"))
	sort.Strings(agents)
	for _, p := range agents {
		var meta struct {
			Description string `yaml:"description"`
			Model       string `yaml:"model"`
		}
		body, err := source.ReadMarkdown(p, &meta)
		if err != nil || meta.Description == "" {
			warn = append(warn, fmt.Sprintf("agent %s skipped: needs frontmatter with a description (%v)", p, err))
			continue
		}
		src.Agents = append(src.Agents, model.Agent{Name: strings.TrimSuffix(filepath.Base(p), ".md"), Description: meta.Description, Model: meta.Model, Prompt: body})
	}

	cfg, err := env.JSONObject(filepath.Join(dir, "opencode.json"))
	if err != nil {
		return nil, nil, err
	}
	src.MCP, w = importMCP(cfg["mcp"])
	warn = append(warn, w...)
	src.Permissions, w = importPermissions(cfg["permission"])
	warn = append(warn, w...)

	plugins, _ := filepath.Glob(filepath.Join(dir, "plugins", "*"))
	sort.Strings(plugins)
	for _, p := range plugins {
		if filepath.Base(p) != pluginFile {
			warn = append(warn, fmt.Sprintf("plugin %s not imported: plugins are code, not hooks", p))
		}
	}
	if b, err := env.ReadOptional(HooksFile(env)); err != nil || b != nil {
		var hs []hookrun.Hook
		if err == nil {
			err = json.Unmarshal(b, &hs)
		}
		if err != nil {
			warn = append(warn, fmt.Sprintf("%s not imported: %v", HooksFile(env), err))
		}
		for _, h := range hs {
			src.Hooks = append(src.Hooks, model.Hook{Name: h.Name, Event: h.Event, Matcher: h.Matcher, Command: h.Command, Timeout: h.Timeout})
		}
	}
	return src, warn, nil
}

// importMCP turns OpenCode's command arrays and "environment" into the shape native reads.
func importMCP(v any) ([]model.MCPServer, []string) {
	servers, _ := v.(map[string]any)
	shaped := map[string]any{}
	for name, raw := range servers {
		s, _ := raw.(map[string]any)
		entry := map[string]any{"url": s["url"], "headers": s["headers"], "env": s["environment"]}
		if enabled, ok := s["enabled"].(bool); ok && !enabled {
			entry["disabled"] = true
		}
		if cmd := native.StringList(s["command"]); len(cmd) > 0 {
			entry["command"] = cmd[0]
			args := make([]any, 0, len(cmd)-1)
			for _, a := range cmd[1:] {
				args = append(args, a)
			}
			entry["args"] = args
		}
		shaped[name] = entry
	}
	return native.MCPServers(shaped, "headers")
}

func importPermissions(v any) ([]model.Permission, []string) {
	perms, _ := v.(map[string]any)
	var out []model.Permission
	var warn []string
	for _, tool := range native.SortedKeys(perms) {
		capability, known := capabilityOf[tool]
		if !known {
			warn = append(warn, fmt.Sprintf("permission %q not imported: no canonical capability", tool))
			continue
		}
		byEffect := map[string][]string{}
		switch rule := perms[tool].(type) {
		case string:
			byEffect[rule] = []string{"*"}
		case map[string]any:
			for _, pattern := range native.SortedKeys(rule) {
				effect, _ := rule[pattern].(string)
				byEffect[effect] = append(byEffect[effect], pattern)
			}
		}
		for _, effect := range []string{model.EffectAllow, model.EffectAsk, model.EffectDeny} {
			if len(byEffect[effect]) > 0 {
				out = append(out, model.Permission{Capability: capability, Match: byEffect[effect], Effect: effect})
			}
		}
	}
	return out, warn
}
