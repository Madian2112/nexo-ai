// Package source loads the canonical source directory into a model.Source.
//
// Layout (every entry is optional):
//
//	nexo.yaml               global settings
//	soul/core.md            always-loaded core of the agent's soul
//	soul/sections/*.md      sections with inclusion frontmatter
//	skills/<name>/SKILL.md  Agent Skills standard
//	mcp.yaml                MCP servers
//	hooks.yaml              hooks
//	permissions.yaml        permission rules
//	agents/*.md             custom agents (frontmatter + prompt body)
package source

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Madian2112/nexo-ai/internal/model"
)

func Load(root string) (*model.Source, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("source directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("source directory: %s is not a directory", abs)
	}
	l := loader{root: abs}
	src := &model.Source{Root: abs}
	steps := []func(*model.Source) error{
		l.settings, l.soul, l.skills, l.mcp, l.hooks, l.permissions, l.agents,
	}
	for _, step := range steps {
		if err := step(src); err != nil {
			return nil, err
		}
	}
	return src, nil
}

type loader struct{ root string }

func (l loader) rel(p string) string {
	r, err := filepath.Rel(l.root, p)
	if err != nil {
		return p
	}
	return r
}

func (l loader) fail(p string, format string, args ...any) error {
	return fmt.Errorf("%s: %s", l.rel(p), fmt.Sprintf(format, args...))
}

// readOptional returns nil content when the file does not exist.
func readOptional(p string) ([]byte, error) {
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return b, err
}

func (l loader) yamlFile(name string, out any) (bool, error) {
	p := filepath.Join(l.root, name)
	b, err := readOptional(p)
	if err != nil || b == nil {
		return false, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	// An empty file decodes as io.EOF: treat it as "no content".
	if err := dec.Decode(out); err != nil && !errors.Is(err, io.EOF) {
		return false, l.fail(p, "%v", err)
	}
	return true, nil
}

func (l loader) settings(src *model.Source) error {
	var f struct {
		Kiro struct {
			ToolSearch *bool `yaml:"toolSearch"`
		} `yaml:"kiro"`
		Models    map[string]map[string]string `yaml:"models"`
		HookAgent map[string]string            `yaml:"hookAgent"`
	}
	if _, err := l.yamlFile("nexo.yaml", &f); err != nil {
		return err
	}
	src.Settings.KiroToolSearch = f.Kiro.ToolSearch == nil || *f.Kiro.ToolSearch
	for alias, tools := range f.Models {
		for tool := range tools {
			if !model.KnownAgent(tool) {
				return l.fail(filepath.Join(l.root, "nexo.yaml"), "models.%s: unknown tool %q (want %s)", alias, tool, strings.Join(model.AgentNames, ", "))
			}
		}
	}
	src.Settings.Models = f.Models
	for tool, as := range f.HookAgent {
		if !model.KnownAgent(tool) {
			return l.fail(filepath.Join(l.root, "nexo.yaml"), "hookAgent: unknown tool %q (want %s)", tool, strings.Join(model.AgentNames, ", "))
		}
		if err := checkName(as); err != nil {
			return l.fail(filepath.Join(l.root, "nexo.yaml"), "hookAgent.%s: %v", tool, err)
		}
	}
	src.Settings.HookAgent = f.HookAgent
	return nil
}

func SplitFrontmatter(b []byte) (meta string, body string, err error) {
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	if !strings.HasPrefix(s, "---\n") {
		return "", s, nil
	}
	rest := s[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", "", errors.New("frontmatter is not closed with ---")
	}
	meta = rest[:end]
	body = strings.TrimPrefix(rest[end+len("\n---"):], "\n")
	return meta, body, nil
}

func (l loader) markdown(p string, meta any) (string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	m, body, err := SplitFrontmatter(b)
	if err != nil {
		return "", l.fail(p, "%v", err)
	}
	if m != "" && meta != nil {
		if err := yaml.Unmarshal([]byte(m), meta); err != nil {
			return "", l.fail(p, "frontmatter: %v", err)
		}
	}
	return body, nil
}

// checkName rejects names that cannot be used as a single file or directory name:
// adapters join them into paths under the user's home.
func checkName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
		return fmt.Errorf("invalid name %q: it must be a single file name, without / or \\", name)
	}
	return nil
}

// names detects two entries of one kind that would compile to the same path.
type names map[string]string

func (n names) add(l loader, p, kind, name string) error {
	if err := checkName(name); err != nil {
		return l.fail(p, "%v", err)
	}
	if first, dup := n[name]; dup {
		return l.fail(p, "%s name %q is already used by %s", kind, name, first)
	}
	n[name] = l.rel(p)
	return nil
}

func sortedGlob(pattern string) ([]string, error) {
	matches, err := filepath.Glob(pattern)
	sort.Strings(matches)
	return matches, err
}

func baseName(p string) string {
	return strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
}

func (l loader) soul(src *model.Source) error {
	core, err := readOptional(filepath.Join(l.root, "soul", "core.md"))
	if err != nil {
		return err
	}
	src.Soul.Core = strings.ReplaceAll(string(core), "\r\n", "\n")

	files, err := sortedGlob(filepath.Join(l.root, "soul", "sections", "*.md"))
	if err != nil {
		return err
	}
	// "core" is taken by soul/core.md, which Kiro installs as steering/nexo-core.md.
	seen := names{"core": "soul/core.md"}
	for _, p := range files {
		var meta struct {
			Name        string   `yaml:"name"`
			Description string   `yaml:"description"`
			Inclusion   string   `yaml:"inclusion"`
			Globs       []string `yaml:"globs"`
			Keywords    []string `yaml:"keywords"`
		}
		body, err := l.markdown(p, &meta)
		if err != nil {
			return err
		}
		sec := model.Section{
			Name:        meta.Name,
			Description: meta.Description,
			Inclusion:   model.Inclusion(meta.Inclusion),
			Globs:       meta.Globs,
			Keywords:    meta.Keywords,
			Body:        body,
		}
		if sec.Name == "" {
			sec.Name = baseName(p)
		}
		if err := seen.add(l, p, "section", sec.Name); err != nil {
			return err
		}
		if sec.Inclusion == "" {
			sec.Inclusion = model.InclusionAlways
		}
		switch sec.Inclusion {
		case model.InclusionAlways:
		case model.InclusionFileMatch:
			if len(sec.Globs) == 0 {
				return l.fail(p, "inclusion fileMatch requires globs")
			}
		case model.InclusionIntent:
			if len(sec.Keywords) == 0 {
				return l.fail(p, "inclusion intent requires keywords")
			}
		default:
			return l.fail(p, "unknown inclusion %q (want always, fileMatch or intent)", meta.Inclusion)
		}
		src.Soul.Sections = append(src.Soul.Sections, sec)
	}
	return nil
}

func (l loader) skills(src *model.Source) error {
	files, err := sortedGlob(filepath.Join(l.root, "skills", "*", "SKILL.md"))
	if err != nil {
		return err
	}
	seen := names{}
	for _, p := range files {
		var meta struct {
			Name        string `yaml:"name"`
			Description string `yaml:"description"`
		}
		if _, err := l.markdown(p, &meta); err != nil {
			return err
		}
		if meta.Name == "" || meta.Description == "" {
			return l.fail(p, "SKILL.md frontmatter requires name and description")
		}
		if err := seen.add(l, p, "skill", meta.Name); err != nil {
			return err
		}
		src.Skills = append(src.Skills, model.Skill{Name: meta.Name, Description: meta.Description, Dir: filepath.Dir(p)})
	}
	return nil
}

func (l loader) mcp(src *model.Source) error {
	var f struct {
		Servers map[string]struct {
			Command string            `yaml:"command"`
			Args    []string          `yaml:"args"`
			Env     map[string]string `yaml:"env"`
			URL     string            `yaml:"url"`
			Headers map[string]string `yaml:"headers"`
		} `yaml:"servers"`
	}
	if _, err := l.yamlFile("mcp.yaml", &f); err != nil {
		return err
	}
	p := filepath.Join(l.root, "mcp.yaml")
	names := make([]string, 0, len(f.Servers))
	for n := range f.Servers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		s := f.Servers[n]
		if (s.Command == "") == (s.URL == "") {
			return l.fail(p, "server %q needs exactly one of command or url", n)
		}
		src.MCP = append(src.MCP, model.MCPServer{Name: n, Command: s.Command, Args: s.Args, Env: s.Env, URL: s.URL, Headers: s.Headers})
	}
	return nil
}

var validEvents = map[string]bool{
	model.EventSessionStart: true, model.EventUserPromptSubmit: true,
	model.EventPreToolUse: true, model.EventPostToolUse: true, model.EventStop: true,
}

var validMatchers = map[string]bool{
	model.MatcherAny: true, "*": true, model.MatcherWrite: true, model.MatcherShell: true, model.MatcherRead: true,
}

func (l loader) hooks(src *model.Source) error {
	var f struct {
		Hooks []struct {
			Name        string   `yaml:"name"`
			Description string   `yaml:"description"`
			Event       string   `yaml:"event"`
			Matcher     string   `yaml:"matcher"`
			Command     string   `yaml:"command"`
			Timeout     int      `yaml:"timeout"`
			Agents      []string `yaml:"agents"`
		} `yaml:"hooks"`
	}
	if _, err := l.yamlFile("hooks.yaml", &f); err != nil {
		return err
	}
	p := filepath.Join(l.root, "hooks.yaml")
	for i, h := range f.Hooks {
		if h.Name == "" || h.Command == "" {
			return l.fail(p, "hook #%d needs name and command", i+1)
		}
		if !validEvents[h.Event] {
			return l.fail(p, "hook %q: unknown event %q", h.Name, h.Event)
		}
		if !validMatchers[h.Matcher] {
			return l.fail(p, "hook %q: unknown matcher %q (want write, shell, read or empty)", h.Name, h.Matcher)
		}
		for _, a := range h.Agents {
			if !model.KnownAgent(a) {
				return l.fail(p, "hook %q: agents: unknown agent %q (want %s)", h.Name, a, strings.Join(model.AgentNames, ", "))
			}
		}
		matcher := h.Matcher
		if matcher == "*" {
			matcher = model.MatcherAny
		}
		src.Hooks = append(src.Hooks, model.Hook{Name: h.Name, Description: h.Description, Event: h.Event, Matcher: matcher, Command: h.Command, Timeout: h.Timeout, Agents: h.Agents})
	}
	return nil
}

func (l loader) permissions(src *model.Source) error {
	var f struct {
		Rules []struct {
			Capability string   `yaml:"capability"`
			Match      []string `yaml:"match"`
			Effect     string   `yaml:"effect"`
		} `yaml:"rules"`
	}
	if _, err := l.yamlFile("permissions.yaml", &f); err != nil {
		return err
	}
	p := filepath.Join(l.root, "permissions.yaml")
	for i, r := range f.Rules {
		switch r.Capability {
		case model.CapabilityRead, model.CapabilityWrite, model.CapabilityShell, model.CapabilityMCP:
		default:
			return l.fail(p, "rule #%d: unknown capability %q (want read, write, shell or mcp)", i+1, r.Capability)
		}
		switch r.Effect {
		case model.EffectAllow, model.EffectAsk, model.EffectDeny:
		default:
			return l.fail(p, "rule #%d: unknown effect %q (want allow, ask or deny)", i+1, r.Effect)
		}
		if len(r.Match) == 0 {
			return l.fail(p, "rule #%d: match must list at least one pattern", i+1)
		}
		src.Permissions = append(src.Permissions, model.Permission{Capability: r.Capability, Match: r.Match, Effect: r.Effect})
	}
	return nil
}

func (l loader) agents(src *model.Source) error {
	files, err := sortedGlob(filepath.Join(l.root, "agents", "*.md"))
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, s := range src.MCP {
		known[s.Name] = true
	}
	seen := names{}
	for _, p := range files {
		var meta struct {
			Name        string   `yaml:"name"`
			Description string   `yaml:"description"`
			Model       string   `yaml:"model"`
			MCP         []string `yaml:"mcp"`
		}
		body, err := l.markdown(p, &meta)
		if err != nil {
			return err
		}
		if meta.Name == "" {
			meta.Name = baseName(p)
		}
		if err := seen.add(l, p, "agent", meta.Name); err != nil {
			return err
		}
		if meta.Description == "" {
			return l.fail(p, "agent frontmatter requires description")
		}
		for _, m := range meta.MCP {
			if !known[m] {
				return l.fail(p, "agent %q references MCP server %q, which is not defined in mcp.yaml", meta.Name, m)
			}
		}
		src.Agents = append(src.Agents, model.Agent{Name: meta.Name, Description: meta.Description, Model: meta.Model, MCP: meta.MCP, Prompt: body})
	}
	return nil
}
