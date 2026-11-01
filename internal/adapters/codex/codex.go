// Package codex compiles a model.Source into Codex CLI's user-level config
// (~/.codex, plus ~/.agents/skills for skills).
package codex

import (
	"bytes"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/Madian2112/nexo-ai/internal/model"
	"github.com/Madian2112/nexo-ai/internal/plan"
)

const Name = "codex"

var matchers = map[string]string{
	model.MatcherWrite: "apply_patch|Edit|Write",
	model.MatcherShell: "Bash",
}

func Plan(src *model.Source, env plan.Env) (plan.Plan, error) {
	p := plan.Plan{Agent: Name}
	dir := filepath.Join(env.Home, ".codex")

	if soul := src.Soul.AlwaysSoul(); soul != "" {
		p.File(filepath.Join(dir, "AGENTS.md"), []byte(soul))
	}
	for _, sec := range src.Soul.Sections {
		switch sec.Inclusion {
		case model.InclusionFileMatch:
			p.Warn("section %q (fileMatch) skipped: Codex has no path-scoped instructions; the router will cover it", sec.Name)
		case model.InclusionIntent:
			p.Warn("section %q (intent) is not installed yet: intent routing arrives with the router", sec.Name)
		}
	}
	for _, s := range src.Skills {
		p.Link(filepath.Join(env.Home, ".agents", "skills", s.Name), s.Dir)
	}
	for _, a := range src.Agents {
		a.Model = p.Model(src.Settings, a)
		b, err := agent(a)
		if err != nil {
			return p, err
		}
		p.File(filepath.Join(dir, "agents", a.Name+".toml"), b)
		if len(a.MCP) > 0 {
			p.Warn("agent %q: per-agent MCP scoping is not applied in Codex; every server stays available", a.Name)
		}
	}

	if hs := hooks(src.Hooks, env.Home, src.Settings.HookAgentFor(Name), &p); len(hs) > 0 {
		p.File(filepath.Join(dir, "hooks.json"), plan.JSON(map[string]any{"hooks": hs}))
		p.Warn("Codex runs new or changed hooks only after you review and trust them with /hooks")
	}
	if len(src.Permissions) > 0 {
		p.Warn("%d permission rules skipped: Codex has no per-path rules; use sandbox_mode and approval_policy in config.toml", len(src.Permissions))
	}

	path := filepath.Join(dir, "config.toml")
	if env.Need(path, len(src.MCP) > 0) {
		b, err := env.ReadOptional(path)
		if err != nil {
			return p, err
		}
		cfg := map[string]any{}
		if len(bytes.TrimSpace(b)) > 0 {
			if err := toml.Unmarshal(b, &cfg); err != nil {
				return p, fmt.Errorf("%s is not valid TOML, refusing to overwrite it: %w", path, err)
			}
		}
		shared := env.Shared(path)
		var servers any
		if len(src.MCP) > 0 {
			servers = mcpServers(src.MCP, env.Home, &p)
		}
		shared.Set(cfg, "mcp_servers", servers)
		if shared.Touched() {
			out, err := toml.Marshal(cfg)
			if err != nil {
				return p, err
			}
			if !bytes.Equal(out, b) && bytes.Contains(b, []byte("#")) {
				p.Warn("config.toml is rewritten without its comments; the original is kept in the backup")
			}
			p.Merged(path, out, shared.Keys)
		}
	}
	return p, nil
}

func agent(a model.Agent) ([]byte, error) {
	cfg := map[string]any{
		"name":                   a.Name,
		"description":            a.Description,
		"developer_instructions": strings.Trim(a.Prompt, "\n") + "\n",
	}
	if a.Model != "" {
		cfg["model"] = a.Model
	}
	return toml.Marshal(cfg)
}

func hooks(hs []model.Hook, home, agentName string, p *plan.Plan) map[string]any {
	out := map[string]any{}
	for _, h := range hs {
		if !h.For(Name) {
			continue
		}
		matcher, mapped := matchers[h.Matcher]
		if h.Matcher != model.MatcherAny && !mapped {
			p.Warn("hook %q skipped: Codex has no %q tool matcher (reads go through the shell)", h.Name, h.Matcher)
			continue
		}
		handler := map[string]any{"type": "command", "command": plan.HookCommand(h.Command, agentName, home)}
		if h.Timeout > 0 {
			handler["timeout"] = h.Timeout
		}
		group := map[string]any{"hooks": []any{handler}}
		if matcher != "" {
			group["matcher"] = matcher
		}
		list, _ := out[h.Event].([]any)
		out[h.Event] = append(list, group)
	}
	return out
}

func firstEnvRef(values []string) (string, bool) {
	for _, v := range values {
		if plan.HasEnvRef(v) {
			return v, true
		}
	}
	return "", false
}

// mcpServers writes Codex's mcp_servers. Codex does not expand variables in config: an
// env value that is exactly {{env:NAME}} becomes NAME in env_vars, the whitelist of
// variables Codex passes through from its own environment.
func mcpServers(servers []model.MCPServer, home string, p *plan.Plan) map[string]any {
	out := map[string]any{}
	for _, s := range servers {
		passThrough := []string{}
		env := map[string]string{}
		for k, v := range s.Env {
			if name, ok := plan.EnvOnly(v); ok && name == k {
				passThrough = append(passThrough, name)
				continue
			}
			env[k] = v
		}
		s.Env = env
		s = plan.ResolveServer(s, home, "{{env:%s}}")
		// Codex would store a leftover reference literally: drop such env and header
		// entries, and report references in the command line, which cannot be dropped.
		for _, m := range []map[string]string{s.Env, s.Headers} {
			for k, v := range m {
				if plan.HasEnvRef(v) {
					p.Warn("MCP server %q: %s not written, Codex cannot read %q from the environment; only env entries written as NAME: {{env:NAME}} pass through", s.Name, k, v)
					delete(m, k)
				}
			}
		}
		if v, found := firstEnvRef(append([]string{s.Command, s.URL}, s.Args...)); found {
			// The server would start with the literal text: leave it out of Codex.
			p.Warn("MCP server %q skipped: Codex cannot read %q from the environment in command, args or url", s.Name, v)
			continue
		}
		if s.Remote() {
			entry := map[string]any{"url": s.URL}
			if len(s.Headers) > 0 {
				entry["http_headers"] = s.Headers
			}
			out[s.Name] = entry
			continue
		}
		entry := map[string]any{"command": s.Command}
		if len(s.Args) > 0 {
			entry["args"] = s.Args
		}
		if len(s.Env) > 0 {
			entry["env"] = s.Env
		}
		if len(passThrough) > 0 {
			sort.Strings(passThrough)
			entry["env_vars"] = passThrough
		}
		out[s.Name] = entry
	}
	return out
}
