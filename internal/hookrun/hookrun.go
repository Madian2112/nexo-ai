// Package hookrun runs hooks for agents without command hooks. OpenCode's plugin is a
// thin shim that hands each event to `nexo-ai hook opencode`; this package maps the
// event to a canonical one, builds a Claude-shaped payload, runs the matching hook
// commands and decides whether the action is blocked.
package hookrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Hook is one resolved hook, as install writes it for the shim's agent.
type Hook struct {
	Name    string `json:"name"`
	Event   string `json:"event"`
	Matcher string `json:"matcher,omitempty"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

const defaultTimeout = 60 * time.Second

// Request is what the OpenCode shim sends on stdin: the plugin hook that fired and the
// fields OpenCode gave it.
type Request struct {
	Hook      string         `json:"hook"`
	Tool      string         `json:"tool,omitempty"`
	SessionID string         `json:"sessionID,omitempty"`
	Args      map[string]any `json:"args,omitempty"`
	Prompt    string         `json:"prompt,omitempty"`
	Directory string         `json:"directory,omitempty"`
}

// Result is what the shim reads back: a non-empty Block makes it throw, which stops the
// tool call or the prompt.
type Result struct {
	Block string `json:"block,omitempty"`
}

var openCodeEvents = map[string]string{
	"tool.execute.before": "PreToolUse",
	"tool.execute.after":  "PostToolUse",
	"chat.message":        "UserPromptSubmit",
	"session.created":     "SessionStart",
	"session.idle":        "Stop",
}

// OpenCode tool names, with the canonical matcher kind and the Claude tool name hooks expect.
var openCodeTools = map[string]struct{ kind, claude string }{
	"bash":        {"shell", "Bash"},
	"edit":        {"write", "Edit"},
	"write":       {"write", "Write"},
	"patch":       {"write", "Edit"},
	"multiedit":   {"write", "MultiEdit"},
	"apply_patch": {"write", "Edit"},
	"read":        {"read", "Read"},
	"grep":        {"read", "Grep"},
	"glob":        {"read", "Glob"},
	"list":        {"read", "LS"},
}

// Runner runs one hook command with stdin and returns its exit code and output.
type Runner func(command string, stdin []byte, timeout time.Duration) (code int, stdout, stderr []byte, err error)

// OpenCode runs the hooks that match an OpenCode plugin event, in order. Only PreToolUse
// and UserPromptSubmit can block; the first hook that blocks stops the rest. A hook that
// fails any other way (exit code other than 2, timeout, cannot start) never blocks.
func OpenCode(req Request, hooks []Hook, run Runner) Result {
	event, ok := openCodeEvents[req.Hook]
	if !ok {
		return Result{}
	}
	tool := openCodeTools[req.Tool]
	payload := map[string]any{"hook_event_name": event, "session_id": req.SessionID, "cwd": req.Directory}
	switch event {
	case "PreToolUse", "PostToolUse":
		name := tool.claude
		if name == "" {
			name = req.Tool
		}
		payload["tool_name"] = name
		payload["tool_input"] = toolInput(req.Args)
	case "UserPromptSubmit":
		payload["prompt"] = req.Prompt
	case "SessionStart":
		payload["source"] = "startup"
	}
	stdin, err := json.Marshal(payload)
	if err != nil {
		return Result{}
	}
	canBlock := event == "PreToolUse" || event == "UserPromptSubmit"
	for _, h := range hooks {
		if h.Event != event || (h.Matcher != "" && h.Matcher != tool.kind) {
			continue
		}
		timeout := defaultTimeout
		if h.Timeout > 0 {
			timeout = time.Duration(h.Timeout) * time.Second
		}
		code, stdout, stderr, err := run(h.Command, stdin, timeout)
		if err != nil || !canBlock {
			continue
		}
		if reason := blockReason(h.Name, code, stdout, stderr); reason != "" {
			return Result{Block: reason}
		}
	}
	return Result{}
}

// toolInput gives OpenCode's camelCase arguments the snake_case names Claude hooks read.
func toolInput(args map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range args {
		out[k] = v
	}
	for from, to := range map[string]string{"filePath": "file_path", "oldString": "old_string", "newString": "new_string"} {
		if v, ok := args[from]; ok {
			out[to] = v
		}
	}
	return out
}

func blockReason(name string, code int, stdout, stderr []byte) string {
	if code == 2 {
		if r := strings.TrimSpace(string(stderr)); r != "" {
			return r
		}
		return "blocked by hook " + name
	}
	var out struct {
		Decision           string `json:"decision"`
		Reason             string `json:"reason"`
		HookSpecificOutput struct {
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if json.Unmarshal(bytes.TrimSpace(stdout), &out) != nil {
		return ""
	}
	if out.HookSpecificOutput.PermissionDecision == "deny" {
		return firstNonEmpty(out.HookSpecificOutput.PermissionDecisionReason, "denied by hook "+name)
	}
	if out.Decision == "block" {
		return firstNonEmpty(out.Reason, "blocked by hook "+name)
	}
	return ""
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Exec runs a hook command through the system shell.
func Exec(command string, stdin []byte, timeout time.Duration) (int, []byte, []byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := shell(ctx, command)
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// Stop waiting for output pipes a killed command's children may still hold.
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if ctx.Err() != nil {
		return -1, nil, nil, fmt.Errorf("hook timed out after %s", timeout)
	}
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		return exit.ExitCode(), stdout.Bytes(), stderr.Bytes(), nil
	case errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil:
		// The hook exited, but a background child still holds its output: its exit code
		// and what it printed until then still decide.
		return cmd.ProcessState.ExitCode(), stdout.Bytes(), stderr.Bytes(), nil
	case err != nil:
		return -1, nil, nil, err
	}
	return 0, stdout.Bytes(), stderr.Bytes(), nil
}

// LoadHooks reads the hooks file install wrote for the shim.
func LoadHooks(path string) ([]Hook, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var hooks []Hook
	if err := json.Unmarshal(b, &hooks); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return hooks, nil
}
