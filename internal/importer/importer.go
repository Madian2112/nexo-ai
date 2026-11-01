// Package importer folds agent configs read by the adapters' Import functions into the
// canonical source. The source always wins: an entry that already exists is kept and a
// differing imported one is reported, so importing never overwrites what you curated.
package importer

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Madian2112/nexo-ai/internal/model"
	"github.com/Madian2112/nexo-ai/internal/plan"
	"github.com/Madian2112/nexo-ai/internal/source"
)

// Options tells Merge where the source and the home are, to refuse skills it must not copy.
type Options struct {
	Root string
	Home string
}

// Merge returns base plus every imported entry base does not have yet.
func Merge(base, imported *model.Source, agent string, opts Options) (*model.Source, []string) {
	m := NewMerger(base, opts)
	warn := m.Add(imported, agent)
	return m.Result(), append(warn, m.Summary()...)
}

// Merger folds several agents into one source. It remembers which agent each hook of
// this run came from, so one hook imported from two agents becomes a single {{agent}} hook.
type Merger struct {
	base   *model.Source
	out    *model.Source
	opts   Options
	origin map[int]string
}

func NewMerger(base *model.Source, opts Options) *Merger {
	return &Merger{base: base, out: clone(base), opts: opts, origin: map[int]string{}}
}

func (m *Merger) Result() *model.Source { return m.out }

// Summary reports the final state once every agent was added: which imported hooks
// stayed scoped to the one agent that had them.
func (m *Merger) Summary() []string {
	scoped := map[string]int{}
	for _, h := range m.out.Hooks[len(m.base.Hooks):] {
		if len(h.Agents) == 1 {
			scoped[h.Agents[0]]++
		}
	}
	var warn []string
	for _, agent := range []string{"claude", "kiro", "codex", "opencode"} {
		if n := scoped[agent]; n > 0 {
			warn = append(warn, fmt.Sprintf("%s: %d hooks only %s has stay scoped to %s (agents: [%s]); remove that line in hooks.yaml to share one with every agent", agent, n, agent, agent, agent))
		}
	}
	return warn
}

func (m *Merger) Add(imported *model.Source, agent string) []string {
	out, base, opts := m.out, m.base, m.opts
	var warn []string

	if core := strings.TrimSpace(imported.Soul.Core); core != "" {
		switch existing := strings.TrimSpace(out.Soul.Core); {
		case existing == "":
			out.Soul.Core = imported.Soul.Core
		case existing != core:
			warn = append(warn, fmt.Sprintf("%s: soul/core.md already exists, the %s soul was not imported; merge it by hand", agent, agent))
		}
	}

	sections := map[string]model.Section{"core": {}}
	for _, s := range out.Soul.Sections {
		sections[s.Name] = s
	}
	for _, s := range imported.Soul.Sections {
		if s.Name == "core" {
			warn = append(warn, fmt.Sprintf("%s: section \"core\" skipped: the name is reserved for soul/core.md; rename it", agent))
			continue
		}
		if !source.ValidName(s.Name) {
			warn = append(warn, fmt.Sprintf("%s: section %q skipped: invalid name", agent, s.Name))
			continue
		}
		if existing, dup := sections[s.Name]; dup {
			if strings.TrimSpace(existing.Body) != strings.TrimSpace(s.Body) {
				warn = append(warn, fmt.Sprintf("%s: section %q already exists in the source, kept the source's", agent, s.Name))
			}
			continue
		}
		sections[s.Name] = s
		out.Soul.Sections = append(out.Soul.Sections, s)
	}

	skills := map[string]model.Skill{}
	for _, s := range out.Skills {
		skills[s.Name] = s
	}
	for _, s := range imported.Skills {
		if existing, dup := skills[s.Name]; dup {
			if !sameDir(existing.Dir, s.Dir) {
				warn = append(warn, fmt.Sprintf("%s: skill %q already exists in the source, kept the source's", agent, s.Name))
			}
			continue
		}
		if err := copyable(s.Dir, opts); err != nil {
			warn = append(warn, fmt.Sprintf("%s: skill %q skipped: %v", agent, s.Name, err))
			continue
		}
		skills[s.Name] = s
		out.Skills = append(out.Skills, s)
	}

	servers := map[string]model.MCPServer{}
	for _, s := range out.MCP {
		servers[s.Name] = s
	}
	secrets := false
	for _, s := range imported.MCP {
		s = homeServer(s, opts.Home)
		if existing, dup := servers[s.Name]; dup {
			if fmt.Sprint(existing) != fmt.Sprint(s) {
				warn = append(warn, fmt.Sprintf("%s: MCP server %q differs from the source's, kept the source's", agent, s.Name))
			}
			continue
		}
		servers[s.Name] = s
		out.MCP = append(out.MCP, s)
		secrets = secrets || len(s.Env) > 0 || len(s.Headers) > 0
	}
	if secrets {
		warn = append(warn, fmt.Sprintf("%s: imported MCP env or headers may hold tokens; mcp.yaml is written 0600, keep the source private", agent))
	}

	agents := map[string]bool{}
	for _, a := range out.Agents {
		agents[a.Name] = true
	}
	for _, a := range imported.Agents {
		if !source.ValidName(a.Name) {
			warn = append(warn, fmt.Sprintf("%s: agent %q skipped: invalid name", agent, a.Name))
			continue
		}
		if agents[a.Name] {
			warn = append(warn, fmt.Sprintf("%s: agent %q already exists in the source, kept the source's", agent, a.Name))
			continue
		}
		var mcp []string
		for _, m := range a.MCP {
			if _, ok := servers[m]; ok {
				mcp = append(mcp, m)
			} else {
				warn = append(warn, fmt.Sprintf("%s: agent %q references MCP server %q, which is not in the source; dropped", agent, a.Name, m))
			}
		}
		a.MCP = mcp
		agents[a.Name] = true
		out.Agents = append(out.Agents, a)
	}

	// The same hook from several agents becomes one hook shared by every agent: identical
	// once the home directory is written as {{home}}, or also once the agent's own name is
	// written as {{agent}}. A hook only one agent has stays scoped to that agent, because
	// its command may only understand that agent's payload.
	type hookKey struct{ event, matcher, command string }
	exact := map[hookKey]int{}
	templated := map[hookKey]int{}
	hookNames := map[string]bool{}
	for i, h := range out.Hooks {
		hookNames[h.Name] = true
		if i < len(base.Hooks) {
			// Source hooks win; compare them as this agent would run them.
			if h.For(agent) {
				exact[hookKey{h.Event, h.Matcher, plan.WithAgent(h.Command, agent)}] = -1
			}
			continue
		}
		exact[hookKey{h.Event, h.Matcher, plan.WithAgent(h.Command, agent)}] = i
		if from := m.origin[i]; from != "" && from != agent && strings.Contains(h.Command, from) {
			templated[hookKey{h.Event, h.Matcher, strings.ReplaceAll(h.Command, from, "{{agent}}")}] = i
		}
	}
	shared := 0
	for _, h := range imported.Hooks {
		h.Command = HomeTemplate(h.Command, opts.Home)
		if i, seen := exact[hookKey{h.Event, h.Matcher, h.Command}]; seen {
			if i >= 0 && m.origin[i] != "" && m.origin[i] != agent {
				out.Hooks[i].Agents = nil
				delete(m.origin, i)
				shared++
			}
			continue
		}
		if strings.Contains(h.Command, agent) {
			if i, seen := templated[hookKey{h.Event, h.Matcher, strings.ReplaceAll(h.Command, agent, "{{agent}}")}]; seen {
				out.Hooks[i].Command = strings.ReplaceAll(out.Hooks[i].Command, m.origin[i], "{{agent}}")
				out.Hooks[i].Agents = nil
				delete(m.origin, i)
				shared++
				continue
			}
		}
		name := h.Name
		for i := 2; hookNames[h.Name]; i++ {
			h.Name = fmt.Sprintf("%s-%d", name, i)
		}
		hookNames[h.Name] = true
		h.Agents = []string{agent}
		m.origin[len(out.Hooks)] = agent
		exact[hookKey{h.Event, h.Matcher, h.Command}] = len(out.Hooks)
		out.Hooks = append(out.Hooks, h)
	}
	if shared > 0 {
		warn = append(warn, fmt.Sprintf("%s: %d hooks match ones from another agent and are now shared by every agent; each command must understand every agent's payload (for example a dispatcher with --harness {{agent}})", agent, shared))
	}

	// One effect per capability and pattern: an imported allow never sits next to the source's deny.
	type permKey struct{ capability, pattern string }
	perms := map[permKey]string{}
	for _, p := range out.Permissions {
		for _, m := range p.Match {
			perms[permKey{p.Capability, m}] = p.Effect
		}
	}
	for _, p := range imported.Permissions {
		var fresh []string
		for _, m := range p.Match {
			k := permKey{p.Capability, m}
			switch effect, seen := perms[k]; {
			case !seen:
				perms[k] = p.Effect
				fresh = append(fresh, m)
			case effect != p.Effect:
				warn = append(warn, fmt.Sprintf("%s: permission %s %s %q skipped: the source already says %s", agent, p.Effect, p.Capability, m, effect))
			}
		}
		if len(fresh) > 0 {
			out.Permissions = append(out.Permissions, model.Permission{Capability: p.Capability, Match: fresh, Effect: p.Effect})
		}
	}
	return warn
}

// Render plans the files that turn base into merged under dir. Data files (mcp.yaml,
// hooks.yaml, permissions.yaml) are rewritten whole when they gain entries.
func Render(base, merged *model.Source, dir string) (plan.Plan, error) {
	p := plan.Plan{Agent: "import"}
	if merged.Soul.Core != base.Soul.Core {
		p.File(filepath.Join(dir, "soul", "core.md"), []byte(merged.Soul.Core))
	}
	for _, s := range merged.Soul.Sections[len(base.Soul.Sections):] {
		p.File(filepath.Join(dir, "soul", "sections", s.Name+".md"), []byte(section(s)))
	}
	for _, s := range merged.Skills[len(base.Skills):] {
		p.Link(filepath.Join(dir, "skills", s.Name), s.Dir)
	}
	for _, a := range merged.Agents[len(base.Agents):] {
		p.File(filepath.Join(dir, "agents", a.Name+".md"), []byte(agentFile(a)))
	}
	if len(merged.MCP) != len(base.MCP) {
		b, err := yaml.Marshal(map[string]any{"servers": mcpFile(merged.MCP)})
		if err != nil {
			return p, err
		}
		p.Merged(filepath.Join(dir, "mcp.yaml"), b, nil)
	}
	if len(merged.Hooks) != len(base.Hooks) {
		b, err := yaml.Marshal(map[string]any{"hooks": hooksFile(merged.Hooks)})
		if err != nil {
			return p, err
		}
		p.Merged(filepath.Join(dir, "hooks.yaml"), b, nil)
	}
	if len(merged.Permissions) != len(base.Permissions) {
		b, err := yaml.Marshal(map[string]any{"rules": permissionsFile(merged.Permissions)})
		if err != nil {
			return p, err
		}
		p.Merged(filepath.Join(dir, "permissions.yaml"), b, nil)
	}
	return p, nil
}

var (
	claudeConfigVar = regexp.MustCompile(`%CLAUDE_CONFIG_DIR%|\$CLAUDE_CONFIG_DIR\b`)
	windowsProfile  = regexp.MustCompile(`(?i)\b[a-z]:[/\\]users[/\\]([^/\\"'\s]+)`)
	homeVariable    = regexp.MustCompile(`(\$\{HOME\}|\$HOME\b|%USERPROFILE%|\$env:USERPROFILE\b)([/\\"'\s]|$)`)
	tildePath       = regexp.MustCompile(`(^|[\s"'=])~/`)
)

// Windows profile folders that are not a person's home.
var sharedProfiles = map[string]bool{"public": true, "default": true, "default user": true, "all users": true}

// HomeTemplate writes every spelling of a home directory in a command as {{home}}:
// a Windows profile path (C:/Users/<name>), $HOME, ${HOME}, %USERPROFILE%, a leading ~/,
// and home, the home of the machine running the import. Install resolves {{home}} again.
// A spelling is replaced only as a whole path segment, so /home/dev2 is not /home/dev.
func HomeTemplate(command, home string) string {
	// CLAUDE_CONFIG_DIR only exists for Claude: write its default, ~/.claude, so the same
	// path works from every agent.
	command = expandClaudeConfigDir(command)
	command = claudeConfigVar.ReplaceAllString(command, "$$HOME/.claude")
	// Match the home in both slash forms without rewriting the rest of the command:
	// backslashes there may be escapes (\" or regex), not path separators.
	if len(home) > 1 {
		forms := map[string]bool{home: true, strings.ReplaceAll(home, "\\", "/"): true, strings.ReplaceAll(home, "/", "\\"): true}
		for form := range forms {
			command = replaceSegment(command, form, "{{home}}")
		}
	}
	command = windowsProfile.ReplaceAllStringFunc(command, func(p string) string {
		if sharedProfiles[strings.ToLower(windowsProfile.FindStringSubmatch(p)[1])] {
			return p
		}
		return "{{home}}"
	})
	command = homeVariable.ReplaceAllString(command, "{{home}}$2")
	return tildePath.ReplaceAllString(command, "${1}{{home}}/")
}

// expandClaudeConfigDir replaces ${CLAUDE_CONFIG_DIR:-default} with its default, which
// may itself hold braces (${HOME}): the closing brace is found by depth, not by the first }.
func expandClaudeConfigDir(s string) string {
	const open = "${CLAUDE_CONFIG_DIR:-"
	for {
		i := strings.Index(s, open)
		if i < 0 {
			return s
		}
		depth, end := 1, -1
		for j := i + len(open); j < len(s) && end < 0; j++ {
			switch s[j] {
			case '{':
				depth++
			case '}':
				if depth--; depth == 0 {
					end = j
				}
			}
		}
		if end < 0 {
			return s
		}
		s = s[:i] + s[i+len(open):end] + s[end+1:]
	}
}

// replaceSegment replaces old only where the next character ends a path segment.
func replaceSegment(s, old, new string) string {
	var b strings.Builder
	for {
		i := strings.Index(s, old)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		rest := s[i+len(old):]
		b.WriteString(s[:i])
		if rest == "" || strings.ContainsRune("/\\\"' ", rune(rest[0])) {
			b.WriteString(new)
		} else {
			b.WriteString(old)
		}
		s = rest
	}
}

func clone(s *model.Source) *model.Source {
	c := *s
	c.Soul.Sections = append([]model.Section(nil), s.Soul.Sections...)
	c.Skills = append([]model.Skill(nil), s.Skills...)
	c.MCP = append([]model.MCPServer(nil), s.MCP...)
	c.Hooks = append([]model.Hook(nil), s.Hooks...)
	c.Permissions = append([]model.Permission(nil), s.Permissions...)
	c.Agents = append([]model.Agent(nil), s.Agents...)
	return &c
}

// copyable refuses skill directories that are unsafe to copy into the source: one that
// holds the source or the home (the copy would recurse or take everything), a repository
// checkout, secrets, or symlinks that would carry links to outside paths into the source.
func copyable(dir string, opts Options) error {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	for _, p := range []string{opts.Root, opts.Home} {
		if p == "" {
			continue
		}
		if rp, err := filepath.EvalSymlinks(p); err == nil {
			p = rp
		}
		if rel, err := filepath.Rel(real, p); err == nil && !strings.HasPrefix(rel, "..") {
			return fmt.Errorf("its directory %s contains %s", real, p)
		}
	}
	return filepath.WalkDir(real, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		switch {
		case d.Type()&os.ModeSymlink != 0:
			return fmt.Errorf("it contains the symlink %s; resolve it and import again", path)
		case d.IsDir() && name == ".git":
			return fmt.Errorf("it is a repository checkout (%s)", path)
		case !d.IsDir() && (name == ".env" || strings.HasPrefix(name, ".env.")):
			return fmt.Errorf("it contains the secrets file %s", path)
		}
		return nil
	})
}

// sameDir reports whether two skill directories are the same skill: the same directory,
// or copies with an identical SKILL.md (agents often keep their own copy of one skill).
func sameDir(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA == nil && errB == nil && ra == rb {
		return true
	}
	sa, errA := os.ReadFile(filepath.Join(a, "SKILL.md"))
	sb, errB := os.ReadFile(filepath.Join(b, "SKILL.md"))
	return errA == nil && errB == nil && bytes.Equal(sa, sb)
}

func frontmatter(v any) string {
	b, err := yaml.Marshal(v)
	if err != nil {
		panic(err)
	}
	return "---\n" + string(b) + "---\n"
}

func section(s model.Section) string {
	meta := struct {
		Inclusion   string   `yaml:"inclusion"`
		Description string   `yaml:"description,omitempty"`
		Globs       []string `yaml:"globs,omitempty,flow"`
		Keywords    []string `yaml:"keywords,omitempty,flow"`
	}{string(s.Inclusion), s.Description, s.Globs, s.Keywords}
	return frontmatter(meta) + "\n" + strings.Trim(s.Body, "\n") + "\n"
}

func agentFile(a model.Agent) string {
	meta := struct {
		Name        string   `yaml:"name"`
		Description string   `yaml:"description"`
		Model       string   `yaml:"model,omitempty"`
		MCP         []string `yaml:"mcp,omitempty,flow"`
	}{a.Name, a.Description, a.Model, a.MCP}
	return frontmatter(meta) + "\n" + strings.Trim(a.Prompt, "\n") + "\n"
}

type mcpServer struct {
	Command string            `yaml:"command,omitempty"`
	Args    []string          `yaml:"args,omitempty,flow"`
	Env     map[string]string `yaml:"env,omitempty"`
	URL     string            `yaml:"url,omitempty"`
	Headers map[string]string `yaml:"headers,omitempty"`
}

func mcpFile(servers []model.MCPServer) map[string]mcpServer {
	out := map[string]mcpServer{}
	for _, s := range servers {
		out[s.Name] = mcpServer{s.Command, s.Args, s.Env, s.URL, s.Headers}
	}
	return out
}

type hook struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description,omitempty"`
	Event       string   `yaml:"event"`
	Matcher     string   `yaml:"matcher,omitempty"`
	Command     string   `yaml:"command"`
	Timeout     int      `yaml:"timeout,omitempty"`
	Agents      []string `yaml:"agents,omitempty,flow"`
}

func hooksFile(hs []model.Hook) []hook {
	out := make([]hook, len(hs))
	for i, h := range hs {
		out[i] = hook{h.Name, h.Description, h.Event, h.Matcher, h.Command, h.Timeout, h.Agents}
	}
	return out
}

type rule struct {
	Capability string   `yaml:"capability"`
	Match      []string `yaml:"match,flow"`
	Effect     string   `yaml:"effect"`
}

func permissionsFile(ps []model.Permission) []rule {
	out := make([]rule, len(ps))
	for i, p := range ps {
		out[i] = rule{p.Capability, p.Match, p.Effect}
	}
	return out
}

func homeServer(s model.MCPServer, home string) model.MCPServer {
	t := func(v string) string { return HomeTemplate(v, home) }
	out := s
	out.Command, out.URL = t(s.Command), t(s.URL)
	out.Args = nil
	for _, a := range s.Args {
		out.Args = append(out.Args, t(a))
	}
	for _, m := range []*map[string]string{&out.Env, &out.Headers} {
		if *m == nil {
			continue
		}
		c := map[string]string{}
		for k, v := range *m {
			c[k] = t(v)
		}
		*m = c
	}
	return out
}
