package adapters

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/Madian2112/nexo-ai/internal/model"
	"github.com/Madian2112/nexo-ai/internal/plan"
)

func TestOpenCode(t *testing.T) {
	home := t.TempDir()
	p := planFor(t, "opencode", home)

	if !strings.Contains(string(op(t, p, ".config/opencode/AGENTS.md", home).Content), "# Persona") {
		t.Error("AGENTS.md should hold core + always sections")
	}
	if !op(t, p, ".config/opencode/skills/tdd", home).IsLink() {
		t.Error("skill should be linked")
	}
	agent := string(op(t, p, ".config/opencode/agents/dotnet-pro.md", home).Content)
	if !strings.HasPrefix(agent, "---\ndescription: \"Senior C#/.NET architect.") || !strings.Contains(agent, "You are a senior") {
		t.Errorf("agent:\n%s", agent)
	}
	if !hasWarning(p, `"stack-dotnet" (fileMatch) skipped`) || !hasWarning(p, "chat.message") {
		t.Errorf("warnings: %v", p.Warnings)
	}

	cfg := op(t, p, ".config/opencode/opencode.json", home)
	m := jsonOf(t, cfg)
	if m["$schema"] != "https://opencode.ai/config.json" {
		t.Error("$schema")
	}
	mcp := m["mcp"].(map[string]any)
	engram := mcp["engram"].(map[string]any)
	if engram["type"] != "local" || strings.Join(toStrings(engram["command"]), " ") != "engram mcp" || engram["enabled"] != true {
		t.Errorf("local server: %v", engram)
	}
	if mcp["microsoft-learn"].(map[string]any)["type"] != "remote" {
		t.Errorf("remote server: %v", mcp["microsoft-learn"])
	}
	// Last matching rule wins in OpenCode: allow, then ask, then deny, in that order.
	text := string(cfg.Content)
	if i, j := strings.Index(text, `"git status*": "allow"`), strings.Index(text, `"git push*": "ask"`); i < 0 || j < i {
		t.Errorf("bash rules out of order:\n%s", text)
	}
	if !strings.Contains(text, `"**/.env": "deny"`) {
		t.Errorf("read deny missing:\n%s", text)
	}

	hooks := string(op(t, p, ".config/opencode/nexo/hooks.json", home).Content)
	if !strings.Contains(hooks, `"command": "node ~/agent-guards/bin/dispatch.js --harness opencode"`) {
		t.Errorf("hooks file:\n%s", hooks)
	}
	plugin := string(op(t, p, ".config/opencode/plugins/nexo-hooks.js", home).Content)
	if strings.Contains(plugin, "__NEXO") || !strings.Contains(plugin, `const NEXO = "nexo-ai"`) || !strings.Contains(plugin, jsonString(filepath.Join(home, ".config", "opencode", "nexo", "hooks.json"))) {
		t.Errorf("plugin shim:\n%s", plugin)
	}
	if !hasWarning(p, "calls nexo-ai from PATH") {
		t.Errorf("warnings: %v", p.Warnings)
	}
}

func TestOpenCodeEnvReferences(t *testing.T) {
	home := t.TempDir()
	f, _ := Get("opencode")
	p, err := f(envSource(), plan.OSEnv(home))
	if err != nil {
		t.Fatal(err)
	}
	content := string(op(t, p, ".config/opencode/opencode.json", home).Content)
	if !strings.Contains(content, `"TFS_BASE": "{env:TFS_BASE}"`) || !strings.Contains(content, filepath.ToSlash(home)+"/.claude/mcp/tfs/server.js") || strings.Contains(content, "{{") {
		t.Errorf("opencode.json:\n%s", content)
	}
}

func toStrings(v any) []string {
	var out []string
	for _, it := range v.([]any) {
		out = append(out, it.(string))
	}
	return out
}

// buildNexo compiles the real nexo-ai binary once for the plugin tests.
func buildNexo(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "nexo-ai")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	out, err := exec.Command("go", "build", "-o", bin, "github.com/Madian2112/nexo-ai/cmd/nexo-ai").CombinedOutput()
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// TestOpenCodePluginRunsHooks drives the generated shim with node against the real
// binary: a write hook that exits 2 blocks with its reason and gets a Claude-shaped
// payload, a shell hook does not run for a write, and a missing binary fails open.
func TestOpenCodePluginRunsHooks(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	dir := t.TempDir()
	payload := filepath.Join(dir, "payload.json")
	shellRan := filepath.Join(dir, "shell-ran")
	block := filepath.Join(dir, "block.js")
	write(t, block, `const fs = require("fs"); fs.writeFileSync(process.argv[2], fs.readFileSync(0)); process.stderr.write("no writes to .cs"); process.exit(2)`)
	src := &model.Source{Hooks: []model.Hook{
		{Name: "guard", Event: model.EventPreToolUse, Matcher: model.MatcherWrite, Command: `node "` + block + `" "` + payload + `"`},
		{Name: "shell", Event: model.EventPreToolUse, Matcher: model.MatcherShell, Command: `node -e "require('fs').writeFileSync('` + filepath.ToSlash(shellRan) + `', 'x')"`},
	}}
	drive := func(bin, hooksFile, content string) string {
		home := t.TempDir()
		env := plan.OSEnv(home)
		env.Executable = bin
		f, _ := Get("opencode")
		p, err := f(src, env)
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range p.Ops {
			write(t, o.Path, string(o.Content))
		}
		shim := string(op(t, p, ".config/opencode/plugins/nexo-hooks.js", home).Content)
		if hooksFile != "" {
			b, _ := json.Marshal(hooksFile)
			shim = regexp.MustCompile(`(?m)^const HOOKS_FILE = .*$`).ReplaceAllString(shim, "const HOOKS_FILE = "+string(b))
		}
		plugin := filepath.Join(home, "nexo-hooks.mjs")
		write(t, plugin, shim)
		write(t, filepath.Join(home, "content.txt"), content)
		driver := filepath.Join(home, "driver.mjs")
		write(t, driver, `import { NexoHooks } from "./nexo-hooks.mjs"
import { readFileSync } from "node:fs"
const content = readFileSync(new URL("./content.txt", import.meta.url), "utf8")
const hooks = await NexoHooks({ directory: "/repo" })
try {
  await hooks["tool.execute.before"]({ tool: "write", sessionID: "s1", callID: "c1" }, { args: { filePath: "A.cs", content } })
  console.log("NOT BLOCKED")
} catch (e) {
  console.log("BLOCKED: " + e.message)
}
`)
		out, err := exec.Command("node", driver).CombinedOutput()
		if err != nil {
			t.Fatalf("node: %v\n%s", err, out)
		}
		return strings.TrimSpace(string(out))
	}

	nexo := buildNexo(t)
	if got := drive(nexo, "", "x"); got != "BLOCKED: no writes to .cs" {
		t.Errorf("plugin output: %s", got)
	}
	var got map[string]any
	b, err := os.ReadFile(payload)
	if err != nil || json.Unmarshal(b, &got) != nil {
		t.Fatalf("payload not received: %v %s", err, b)
	}
	input := got["tool_input"].(map[string]any)
	if got["hook_event_name"] != "PreToolUse" || got["tool_name"] != "Write" || input["file_path"] != "A.cs" || got["cwd"] != "/repo" {
		t.Errorf("payload: %v", got)
	}
	if _, err := os.Stat(shellRan); !os.IsNotExist(err) {
		t.Error("a shell hook must not run for a write")
	}
	if got := drive(filepath.Join(dir, "missing-nexo-ai"), "", "x"); !strings.HasSuffix(got, "NOT BLOCKED") || !strings.Contains(got, "nexo-ai hooks are not running") {
		t.Errorf("a missing binary must fail open and say so, got %s", got)
	}
	// A large write must not crash node when the binary answers without reading stdin.
	big := strings.Repeat("x", 2<<20)
	if got := drive(nexo, filepath.Join(dir, "missing-hooks.json"), big); got != "NOT BLOCKED" {
		t.Errorf("a missing hooks file with a 2 MB payload must fail open, got %s", got)
	}
	if truth, err := exec.LookPath("true"); err == nil {
		if got := drive(truth, "", big); got != "NOT BLOCKED" {
			t.Errorf("a binary that never reads stdin must fail open, got %s", got)
		}
	}
}

func TestOpenCodeDropsPermissionToolsTheSourceNoLongerHas(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, ".config", "opencode", "opencode.json"), `{
	  // user comment
	  "theme": "dark",
	  "permission": {"bash": {"git push*": "ask"}, "webfetch": "allow",},
	}`)
	src := &model.Source{Permissions: []model.Permission{{Capability: model.CapabilityRead, Match: []string{"**/.env"}, Effect: model.EffectDeny}}}
	f, _ := Get("opencode")
	env := plan.OSEnv(home)
	env.OwnedKeys = map[string][]string{filepath.Join(home, ".config", "opencode", "opencode.json"): {"permission.rules"}}
	p, err := f(src, env)
	if err != nil {
		t.Fatalf("JSONC must be accepted: %v", err)
	}
	cfg := jsonOf(t, op(t, p, ".config/opencode/opencode.json", home))
	perms := cfg["permission"].(map[string]any)
	if _, stale := perms["bash"]; stale || perms["webfetch"] != "allow" || perms["read"] == nil || cfg["theme"] != "dark" {
		t.Errorf("opencode.json: %v", cfg)
	}
	if !hasWarning(p, "without its comments") {
		t.Errorf("warnings: %v", p.Warnings)
	}
}

func TestOpenCodeHonorsXDGConfigHome(t *testing.T) {
	home := t.TempDir()
	xdg := filepath.Join(home, "xdg")
	env := plan.OSEnv(home)
	env.ConfigHome = xdg
	f, _ := Get("opencode")
	p, err := f(loadExample(t), env)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range p.Ops {
		if !strings.HasPrefix(o.Path, filepath.Join(xdg, "opencode")+string(filepath.Separator)) {
			t.Errorf("%s is outside $XDG_CONFIG_HOME/opencode", o.Path)
		}
	}
	if !strings.Contains(string(op(t, p, "xdg/opencode/plugins/nexo-hooks.js", home).Content), jsonString(filepath.Join(xdg, "opencode", "nexo", "hooks.json"))) {
		t.Error("the shim must point at the hooks file under $XDG_CONFIG_HOME")
	}
}

// jsonString is how the shim embeds a path: JSON-escaped, so Windows backslashes are doubled.
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
