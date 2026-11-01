// Package opencode compiles a model.Source into OpenCode's user-level config
// (~/.config/opencode).
package opencode

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Madian2112/nexo-ai/internal/hookrun"
	"github.com/Madian2112/nexo-ai/internal/model"
	"github.com/Madian2112/nexo-ai/internal/plan"
)

const Name = "opencode"

//go:embed plugin.js
var pluginTemplate string

const pluginFile = "nexo-hooks.js"

// permissionsKey is the owned key recorded in state: read/edit/bash inside "permission".
const permissionsKey = "permission.rules"

var permissionTool = map[string]string{
	model.CapabilityRead:  "read",
	model.CapabilityWrite: "edit",
	model.CapabilityShell: "bash",
}

// OpenCode ignores skills whose name is not lowercase alphanumeric with single hyphens.
var skillName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Dir is OpenCode's config directory: $XDG_CONFIG_HOME/opencode when set, on every OS,
// else ~/.config/opencode (also on Windows).
func Dir(env plan.Env) string {
	if env.ConfigHome != "" {
		return filepath.Join(env.ConfigHome, "opencode")
	}
	return filepath.Join(env.Home, ".config", "opencode")
}

func Plan(src *model.Source, env plan.Env) (plan.Plan, error) {
	p := plan.Plan{Agent: Name}
	dir := Dir(env)

	if soul := src.Soul.AlwaysSoul(); soul != "" {
		p.File(filepath.Join(dir, "AGENTS.md"), []byte(soul))
	}
	for _, sec := range src.Soul.Sections {
		switch sec.Inclusion {
		case model.InclusionFileMatch:
			p.Warn("section %q (fileMatch) skipped: OpenCode has no path-scoped instructions; the router will cover it", sec.Name)
		case model.InclusionIntent:
			p.Warn("section %q (intent) is not installed yet: intent routing arrives with the router", sec.Name)
		}
	}
	for _, s := range src.Skills {
		if !skillName.MatchString(s.Name) {
			p.Warn("skill %q: OpenCode ignores skill names that are not lowercase letters, digits and single hyphens", s.Name)
		}
		p.Link(filepath.Join(dir, "skills", s.Name), s.Dir)
	}
	for _, a := range src.Agents {
		a.Model = p.Model(src.Settings, a)
		p.File(filepath.Join(dir, "agents", a.Name+".md"), []byte(agent(a, &p)))
	}

	if hs := hooks(src.Hooks, env.Home, src.Settings.HookAgentFor(Name), &p); len(hs) > 0 {
		// The rules live outside plugins/: OpenCode loads every file in that directory.
		hooksFile := HooksFile(env)
		p.File(hooksFile, plan.JSON(hs))
		bin := env.Executable
		if bin == "" {
			bin = "nexo-ai"
			p.Warn("the OpenCode plugin calls nexo-ai from PATH: the path of this binary is unknown")
		}
		js := strings.NewReplacer("__NEXO_BIN__", jsString(bin), "__NEXO_HOOKS_FILE__", jsString(hooksFile)).Replace(pluginTemplate)
		p.File(filepath.Join(dir, "plugins", pluginFile), []byte(js))
	}

	path := filepath.Join(dir, "opencode.json")
	if env.Need(path, len(src.MCP) > 0 || len(src.Permissions) > 0) {
		cfg, hadComments, err := jsonc(env, path)
		if err != nil {
			return p, err
		}
		if hadComments {
			p.Warn("opencode.json is rewritten without its comments; the original is kept in the backup")
		}
		shared := env.Shared(path)
		var servers any
		if len(src.MCP) > 0 {
			servers = mcpServers(src.MCP, env.Home)
		}
		shared.Set(cfg, "mcp", servers)
		setPermissions(cfg, shared, src.Permissions, &p)
		if shared.Touched() {
			if _, ok := cfg["$schema"]; !ok {
				cfg["$schema"] = "https://opencode.ai/config.json"
			}
			p.Merged(path, plan.JSON(cfg), shared.Keys)
		}
	}
	return p, nil
}

func agent(a model.Agent, p *plan.Plan) string {
	var b strings.Builder
	b.WriteString("---\ndescription: " + quote(a.Description) + "\n")
	switch {
	case strings.Contains(a.Model, "/"):
		b.WriteString("model: " + a.Model + "\n")
	case a.Model != "":
		p.Warn("agent %q: model %q not written, OpenCode expects provider/model", a.Name, a.Model)
	}
	if len(a.MCP) > 0 {
		p.Warn("agent %q: per-agent MCP scoping is not applied in OpenCode; every server stays available", a.Name)
	}
	b.WriteString("---\n\n" + strings.Trim(a.Prompt, "\n") + "\n")
	return b.String()
}

func quote(s string) string {
	return "\"" + strings.ReplaceAll(strings.ReplaceAll(s, "\\", "\\\\"), "\"", "\\\"") + "\""
}

// HooksFile is where install writes the resolved hooks the plugin hands to nexo-ai.
func HooksFile(env plan.Env) string { return filepath.Join(Dir(env), "nexo", "hooks.json") }

func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func hooks(hs []model.Hook, home, agentName string, p *plan.Plan) []hookrun.Hook {
	out := []hookrun.Hook{}
	for _, h := range hs {
		if !h.For(Name) {
			continue
		}
		out = append(out, hookrun.Hook{Name: h.Name, Event: h.Event, Matcher: h.Matcher, Command: plan.HookCommand(h.Command, agentName, home), Timeout: h.Timeout})
	}
	if len(out) > 0 {
		p.Warn("OpenCode runs the hooks through plugins/%s, which calls `nexo-ai hook opencode`; reinstall if you move the binary; UserPromptSubmit relies on the chat.message plugin hook", pluginFile)
	}
	return out
}

func mcpServers(servers []model.MCPServer, home string) map[string]any {
	out := map[string]any{}
	for _, s := range servers {
		s = plan.ResolveServer(s, home, "{env:%s}")
		if s.Remote() {
			entry := map[string]any{"type": "remote", "url": s.URL, "enabled": true}
			if len(s.Headers) > 0 {
				entry["headers"] = s.Headers
			}
			out[s.Name] = entry
			continue
		}
		entry := map[string]any{"type": "local", "command": append([]string{s.Command}, s.Args...), "enabled": true}
		if len(s.Env) > 0 {
			entry["environment"] = s.Env
		}
		out[s.Name] = entry
	}
	return out
}

// setPermissions replaces read/edit/bash in "permission" and keeps every other key.
// Rules are written allow, then ask, then deny: OpenCode applies the last matching rule,
// so a deny wins over an allow for the same path, as it does in Claude Code.
func setPermissions(cfg map[string]any, shared *plan.Shared, rules []model.Permission, p *plan.Plan) {
	perms, _ := cfg["permission"].(map[string]any)
	if len(rules) == 0 {
		if perms == nil || !shared.WasOwned(permissionsKey) {
			return
		}
		for _, tool := range permissionTool {
			delete(perms, tool)
		}
		if len(perms) == 0 {
			delete(cfg, "permission")
		}
		return
	}
	// read/edit/bash are nexo-ai's as a whole: a tool the source no longer has rules for
	// is removed, not left with old rules.
	out := map[string]any{}
	for k, v := range perms {
		out[k] = v
	}
	for _, tool := range permissionTool {
		delete(out, tool)
	}
	byTool := map[string]plan.Ordered{}
	for _, effect := range []string{model.EffectAllow, model.EffectAsk, model.EffectDeny} {
		for _, r := range rules {
			if r.Effect != effect {
				continue
			}
			tool, ok := permissionTool[r.Capability]
			if !ok {
				p.Warn("permission %s %s %v skipped: OpenCode has no per-server MCP permission rules", r.Effect, r.Capability, r.Match)
				continue
			}
			for _, pattern := range r.Match {
				byTool[tool] = append(byTool[tool], plan.KV{Key: pattern, Value: r.Effect})
			}
		}
	}
	for tool, rules := range byTool {
		out[tool] = rules
	}
	cfg["permission"] = out
	shared.Own(permissionsKey)
}

// jsonc reads opencode.json, which OpenCode accepts with comments and trailing commas.
func jsonc(env plan.Env, path string) (map[string]any, bool, error) {
	b, err := env.ReadOptional(path)
	if err != nil {
		return nil, false, err
	}
	clean, hadComments := stripJSONC(b)
	cfg := map[string]any{}
	if len(bytes.TrimSpace(clean)) == 0 {
		return cfg, false, nil
	}
	if err := json.Unmarshal(clean, &cfg); err != nil {
		return nil, false, fmt.Errorf("%s is not valid JSON or JSONC, refusing to overwrite it: %w", path, err)
	}
	return cfg, hadComments, nil
}

// stripJSONC removes // and /* */ comments and commas before } or ], all outside strings.
func stripJSONC(b []byte) ([]byte, bool) {
	var out []byte
	comments := false
	inString, escaped := false, false
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case inString:
			out = append(out, c)
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
		case c == '"':
			inString = true
			out = append(out, c)
		case c == '/' && i+1 < len(b) && b[i+1] == '/':
			comments = true
			for i < len(b) && b[i] != '\n' {
				i++
			}
			if i < len(b) {
				out = append(out, '\n')
			}
		case c == '/' && i+1 < len(b) && b[i+1] == '*':
			comments = true
			end := bytes.Index(b[i+2:], []byte("*/"))
			if end < 0 {
				return out, comments
			}
			i += end + 3
		case c == '}' || c == ']':
			// Drop a trailing comma: the last non-space byte written, outside any string.
			k := len(out) - 1
			for k >= 0 && (out[k] == ' ' || out[k] == '\t' || out[k] == '\n' || out[k] == '\r') {
				k--
			}
			if k >= 0 && out[k] == ',' {
				out = append(out[:k], out[k+1:]...)
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return out, comments
}
