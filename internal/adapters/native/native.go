// Package native holds helpers that read untyped native config (decoded JSON or TOML)
// back into canonical values. It is shared by the adapters' Import functions.
package native

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/Madian2112/nexo-ai/internal/model"
	"github.com/Madian2112/nexo-ai/internal/source"
)

// StringList accepts a list of strings or a comma-separated string.
func StringList(v any) []string {
	switch t := v.(type) {
	case string:
		var out []string
		for _, s := range strings.Split(t, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out
	case []any:
		var out []string
		for _, it := range t {
			if s, ok := it.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return t
	}
	return nil
}

func StringMap(v any) map[string]string {
	m, _ := v.(map[string]any)
	if len(m) == 0 {
		return nil
	}
	out := map[string]string{}
	for k, val := range m {
		out[k] = fmt.Sprint(val)
	}
	return out
}

// SortedKeys returns the keys of a decoded object in order.
func SortedKeys(v any) []string {
	m, _ := v.(map[string]any)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// MCPServers reads a name → server object map. headersKey is where the agent keeps
// remote headers ("headers", or "http_headers" in Codex). Disabled servers are skipped.
func MCPServers(v any, headersKey string) ([]model.MCPServer, []string) {
	servers, _ := v.(map[string]any)
	var out []model.MCPServer
	var warn []string
	for _, n := range SortedKeys(v) {
		s, _ := servers[n].(map[string]any)
		if disabled, _ := s["disabled"].(bool); disabled {
			warn = append(warn, fmt.Sprintf("MCP server %q skipped: it is disabled", n))
			continue
		}
		server := model.MCPServer{Name: n, Args: StringList(s["args"]), Env: StringMap(s["env"]), Headers: StringMap(s[headersKey])}
		server.Command, _ = s["command"].(string)
		server.URL, _ = s["url"].(string)
		server = EnvTemplate(server)
		if !source.ValidName(n) {
			warn = append(warn, fmt.Sprintf("MCP server %q skipped: invalid name", n))
			continue
		}
		if (server.Command == "") == (server.URL == "") {
			warn = append(warn, fmt.Sprintf("MCP server %q skipped: needs exactly one of command or url", n))
			continue
		}
		out = append(out, server)
	}
	return out, warn
}

var CanonicalEvents = map[string]bool{
	model.EventSessionStart: true, model.EventUserPromptSubmit: true,
	model.EventPreToolUse: true, model.EventPostToolUse: true, model.EventStop: true,
}

// SectionName turns a generated file name back into a section name.
func SectionName(file string) string {
	return strings.TrimPrefix(strings.TrimSuffix(file, ".md"), "nexo-")
}

// MatcherKinds maps a native "Tool|Tool" matcher to its canonical kinds (write, shell,
// read), in that order, using toolKind for each tool. A matcher naming several kinds
// yields several kinds: the caller imports one hook per kind instead of dropping it.
// An empty or "*" matcher is the single kind "any". A tool without a kind fails.
func MatcherKinds(matcher string, toolKind map[string]string) ([]string, bool) {
	if matcher == "" || matcher == "*" {
		return []string{model.MatcherAny}, true
	}
	seen := map[string]bool{}
	for _, tool := range strings.Split(matcher, "|") {
		kind, ok := toolKind[tool]
		if !ok {
			return nil, false
		}
		seen[kind] = true
	}
	var kinds []string
	for _, k := range []string{model.MatcherWrite, model.MatcherShell, model.MatcherRead} {
		if seen[k] {
			kinds = append(kinds, k)
		}
	}
	return kinds, true
}

var canonicalEnv = regexp.MustCompile(`\{\{env:[A-Za-z_][A-Za-z0-9_]*\}\}`)

func toCanonical(v string) string {
	return envReference.ReplaceAllStringFunc(v, func(ref string) string {
		m := envReference.FindStringSubmatch(ref)
		return "{{env:" + m[1] + m[2] + "}}"
	})
}

var envReference = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\{env:([A-Za-z_][A-Za-z0-9_]*)\}`)

// EnvTemplate writes an agent's environment references (${NAME} in Claude and Kiro,
// {env:NAME} in OpenCode) as the canonical {{env:NAME}}.
func EnvTemplate(s model.MCPServer) model.MCPServer {
	t := func(v string) string {
		// Leave canonical {{env:NAME}} alone: {env:NAME} also matches inside it.
		var b strings.Builder
		for {
			loc := canonicalEnv.FindStringIndex(v)
			if loc == nil {
				b.WriteString(toCanonical(v))
				return b.String()
			}
			b.WriteString(toCanonical(v[:loc[0]]))
			b.WriteString(v[loc[0]:loc[1]])
			v = v[loc[1]:]
		}
	}
	s.Command, s.URL = t(s.Command), t(s.URL)
	for i, a := range s.Args {
		s.Args[i] = t(a)
	}
	for _, m := range []map[string]string{s.Env, s.Headers} {
		for k, v := range m {
			m[k] = t(v)
		}
	}
	return s
}
