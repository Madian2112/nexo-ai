// Package model is the canonical, agent-agnostic description of an agent setup.
// Adapters translate it into each agent's native configuration.
package model

import (
	"fmt"
	"strings"
)

type Inclusion string

const (
	InclusionAlways    Inclusion = "always"
	InclusionFileMatch Inclusion = "fileMatch"
	InclusionIntent    Inclusion = "intent"
)

// Section is one piece of the agent's soul (persona, rules, stack guidance).
type Section struct {
	Name        string
	Description string
	Inclusion   Inclusion
	Globs       []string
	Keywords    []string
	Body        string
}

type Soul struct {
	Core     string
	Sections []Section
}

// Skill follows the Agent Skills standard: a directory holding SKILL.md.
type Skill struct {
	Name        string
	Description string
	Dir         string
}

type MCPServer struct {
	Name    string
	Command string
	Args    []string
	Env     map[string]string
	URL     string
	Headers map[string]string
}

func (s MCPServer) Remote() bool { return s.URL != "" }

const (
	EventSessionStart     = "SessionStart"
	EventUserPromptSubmit = "UserPromptSubmit"
	EventPreToolUse       = "PreToolUse"
	EventPostToolUse      = "PostToolUse"
	EventStop             = "Stop"
)

const (
	MatcherAny   = ""
	MatcherWrite = "write"
	MatcherShell = "shell"
	MatcherRead  = "read"
)

// Hook runs Command on Event. Command may contain {{agent}}, replaced by the target agent name.
// Agents limits the hook to those agents; empty means every agent.
type Hook struct {
	Name        string
	Description string
	Event       string
	Matcher     string
	Command     string
	Timeout     int
	Agents      []string
}

// AgentNames lists every agent nexo-ai can configure; the adapters registry matches it.
var AgentNames = []string{"claude", "codex", "kiro", "opencode"}

func KnownAgent(name string) bool {
	for _, a := range AgentNames {
		if a == name {
			return true
		}
	}
	return false
}

// For reports whether the hook is installed into agent.
func (h Hook) For(agent string) bool {
	if len(h.Agents) == 0 {
		return true
	}
	for _, a := range h.Agents {
		if a == agent {
			return true
		}
	}
	return false
}

const (
	CapabilityRead  = "read"
	CapabilityWrite = "write"
	CapabilityShell = "shell"
	CapabilityMCP   = "mcp"
)

const (
	EffectAllow = "allow"
	EffectAsk   = "ask"
	EffectDeny  = "deny"
)

type Permission struct {
	Capability string
	Match      []string
	Effect     string
}

type Agent struct {
	Name        string
	Description string
	Model       string
	MCP         []string
	Prompt      string
}

type Settings struct {
	KiroToolSearch bool
	// Models maps a model alias used by agents (opus) to each tool's model (claude: opus,
	// kiro: claude-opus-5.5, ...). A tool without an entry uses its default model.
	Models map[string]map[string]string
	// HookAgent sets what {{agent}} becomes in hook commands for a tool; the tool's own
	// name by default. OpenCode sends Claude-shaped payloads, so opencode: claude fits.
	HookAgent map[string]string
}

// HookAgentFor is the value of {{agent}} in hook commands for tool.
func (s Settings) HookAgentFor(tool string) string {
	if a := s.HookAgent[tool]; a != "" {
		return a
	}
	return tool
}

// ModelFor returns the model to write in tool. An alias from Settings.Models
// gives that tool's model, or "" with a reason when the tool has none. A model that is
// not an alias is returned as written.
func (s Settings) ModelFor(model, tool string) (string, string) {
	tools, alias := s.Models[model]
	if !alias {
		return model, ""
	}
	if m := tools[tool]; m != "" {
		return m, ""
	}
	return "", fmt.Sprintf("model alias %q has no %s model in nexo.yaml; %s uses its default", model, tool, tool)
}

type Source struct {
	Root        string
	Settings    Settings
	Soul        Soul
	Skills      []Skill
	MCP         []MCPServer
	Hooks       []Hook
	Permissions []Permission
	Agents      []Agent
}

// AlwaysSoul joins the core and every always-included section, in order.
func (s Soul) AlwaysSoul() string {
	parts := []string{}
	if s.Core != "" {
		parts = append(parts, s.Core)
	}
	for _, sec := range s.Sections {
		if sec.Inclusion == InclusionAlways {
			parts = append(parts, sec.Body)
		}
	}
	return joinBlocks(parts)
}

func joinBlocks(parts []string) string {
	trimmed := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.Trim(p, "\r\n"); t != "" {
			trimmed = append(trimmed, t)
		}
	}
	if len(trimmed) == 0 {
		return ""
	}
	return strings.Join(trimmed, "\n\n") + "\n"
}
