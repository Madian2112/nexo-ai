// Package plan holds what an adapter wants on disk. Adapters only compute plans;
// the install package is the only code that touches the filesystem.
package plan

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Madian2112/nexo-ai/internal/model"
)

// Op is one desired filesystem entry.
//
// Exactly one of Content or LinkTo is meaningful. Owned means nexo-ai owns the whole
// entry, so it is removed when it disappears from the source. Shared files that nexo-ai
// only merges keys into (settings.json, config.toml) are not Owned.
//
// Keys lists, for a shared file, the keys nexo-ai owns after this op. They are recorded
// in the install state so a later install can drop them when the source stops defining them.
type Op struct {
	Path    string
	Content []byte
	LinkTo  string
	Owned   bool
	Keys    []string
}

func (o Op) IsLink() bool { return o.LinkTo != "" }

type Plan struct {
	Agent    string
	Ops      []Op
	Warnings []string
}

// Validate rejects plans where two ops target the same path: one would silently
// overwrite the other on every install.
func (p Plan) Validate() error {
	seen := map[string]bool{}
	for _, o := range p.Ops {
		if seen[o.Path] {
			return fmt.Errorf("%s: two source entries compile to the same path %s", p.Agent, o.Path)
		}
		seen[o.Path] = true
	}
	return nil
}

func (p *Plan) File(path string, content []byte) {
	p.Ops = append(p.Ops, Op{Path: path, Content: content, Owned: true})
}

func (p *Plan) Merged(path string, content []byte, keys []string) {
	p.Ops = append(p.Ops, Op{Path: path, Content: content, Keys: keys})
}

func (p *Plan) Link(path, target string) {
	p.Ops = append(p.Ops, Op{Path: path, LinkTo: target, Owned: true})
}

func (p *Plan) Warn(format string, args ...any) {
	p.Warnings = append(p.Warnings, fmt.Sprintf(format, args...))
}

// Env is what an adapter may know about the machine.
//
// OwnedKeys maps a shared file to the keys a previous install wrote into it.
type Env struct {
	Home      string
	ReadFile  func(string) ([]byte, error)
	OwnedKeys map[string][]string
	// Executable is the nexo-ai binary, for generated files that call it back.
	Executable string
	// ConfigHome is $XDG_CONFIG_HOME when set: OpenCode reads it on every OS.
	ConfigHome string
}

func OSEnv(home string) Env {
	return Env{Home: home, ReadFile: os.ReadFile, ConfigHome: os.Getenv("XDG_CONFIG_HOME")}
}

func (e Env) Owned(path, key string) bool {
	for _, k := range e.OwnedKeys[path] {
		if k == key {
			return true
		}
	}
	return false
}

// Shared tracks the owned keys of one shared file while an adapter edits it.
type Shared struct {
	Path string
	Keys []string
	env  Env
}

func (e Env) Shared(path string) *Shared { return &Shared{Path: path, env: e} }

// Touched reports whether the file must be rewritten: nexo-ai owns keys in it now or did before.
func (s *Shared) Touched() bool { return len(s.Keys) > 0 || len(s.env.OwnedKeys[s.Path]) > 0 }

// Set decides one owned key. A non-nil value is written. A nil value drops the key
// only when a previous install wrote it; a value the user set by hand is left alone.
func (s *Shared) Set(obj map[string]any, key string, value any) {
	if value != nil {
		obj[key] = value
		s.Keys = append(s.Keys, key)
		return
	}
	if s.env.Owned(s.Path, key) {
		delete(obj, key)
	}
}

// Own records a key the adapter wrote with custom logic (for nested keys).
func (s *Shared) Own(key string) { s.Keys = append(s.Keys, key) }

// WasOwned reports whether a previous install wrote key into this file.
func (s *Shared) WasOwned(key string) bool { return s.env.Owned(s.Path, key) }

// Need reports whether the file has to be read: the source defines something for it,
// or a previous install owned keys in it.
func (e Env) Need(path string, defined bool) bool {
	return defined || len(e.OwnedKeys[path]) > 0
}

// ReadOptional returns nil content when the file does not exist.
func (e Env) ReadOptional(path string) ([]byte, error) {
	b, err := e.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return b, err
}

// JSONObject reads a JSON object file, or an empty object when it does not exist.
// An unparseable file is an error: nexo-ai never overwrites a file it cannot read.
func (e Env) JSONObject(path string) (map[string]any, error) {
	b, err := e.ReadOptional(path)
	if err != nil {
		return nil, err
	}
	obj := map[string]any{}
	if len(bytes.TrimSpace(b)) == 0 {
		return obj, nil
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return nil, fmt.Errorf("%s is not a valid JSON object, refusing to overwrite it: %w", path, err)
	}
	return obj, nil
}

func JSON(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// HookCommand resolves a hook command for one agent on one machine: {{agent}} becomes
// the agent name and {{home}} the home directory, with forward slashes, which every
// agent's shell accepts on Windows too.
func HookCommand(command, agent, home string) string {
	return strings.ReplaceAll(WithAgent(command, agent), "{{home}}", filepath.ToSlash(home))
}

// WithAgent resolves only {{agent}}, keeping {{home}} portable.
func WithAgent(command, agent string) string {
	return strings.ReplaceAll(command, "{{agent}}", agent)
}

var envRef = regexp.MustCompile(`\{\{env:([A-Za-z_][A-Za-z0-9_]*)\}\}`)

// Resolve prepares a canonical config value for one agent: {{home}} becomes the home
// directory, and each {{env:NAME}} becomes that agent's own environment reference,
// built with envFormat ("${%s}" for Claude and Kiro, "{env:%s}" for OpenCode), so the
// value is read from the environment when the agent starts and never stored in config.
func Resolve(value, home, envFormat string) string {
	value = strings.ReplaceAll(value, "{{home}}", filepath.ToSlash(home))
	return envRef.ReplaceAllStringFunc(value, func(ref string) string {
		return fmt.Sprintf(envFormat, envRef.FindStringSubmatch(ref)[1])
	})
}

// EnvOnly returns NAME when value is exactly {{env:NAME}}.
func EnvOnly(value string) (string, bool) {
	m := envRef.FindStringSubmatch(value)
	if m == nil || m[0] != value {
		return "", false
	}
	return m[1], true
}

// HasEnvRef reports whether value contains a {{env:NAME}} reference.
func HasEnvRef(value string) bool { return envRef.MatchString(value) }

// ResolveServer resolves every string field of an MCP server for one agent.
func ResolveServer(s model.MCPServer, home, envFormat string) model.MCPServer {
	r := func(v string) string { return Resolve(v, home, envFormat) }
	out := s
	out.Command, out.URL = r(s.Command), r(s.URL)
	out.Args = nil
	for _, a := range s.Args {
		out.Args = append(out.Args, r(a))
	}
	out.Env, out.Headers = resolveMap(s.Env, r), resolveMap(s.Headers, r)
	return out
}

func resolveMap(m map[string]string, r func(string) string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = r(v)
	}
	return out
}

// Ordered is a JSON object that keeps its keys in insertion order, for agents where the
// order of rules matters (OpenCode permissions: the last matching rule wins).
type Ordered []KV

type KV struct {
	Key   string
	Value any
}

func (o Ordered) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, kv := range o {
		if i > 0 {
			buf.WriteByte(',')
		}
		k, err := json.Marshal(kv.Key)
		if err != nil {
			return nil, err
		}
		v, err := json.Marshal(kv.Value)
		if err != nil {
			return nil, err
		}
		buf.Write(k)
		buf.WriteByte(':')
		buf.Write(v)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// Model resolves an agent's model for this plan's tool through the nexo.yaml alias table,
// and reports when the tool has no model for that alias.
func (p *Plan) Model(s model.Settings, a model.Agent) string {
	m, reason := s.ModelFor(a.Model, p.Agent)
	if reason != "" {
		p.Warn("agent %q: %s", a.Name, reason)
	}
	return m
}
