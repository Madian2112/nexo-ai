package hookrun

import (
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"
)

type call struct {
	command string
	payload map[string]any
}

// fake answers per command and records what ran.
func fake(answers map[string]struct {
	code   int
	stdout string
	stderr string
	err    error
}, calls *[]call) Runner {
	return func(command string, stdin []byte, _ time.Duration) (int, []byte, []byte, error) {
		var p map[string]any
		_ = json.Unmarshal(stdin, &p)
		*calls = append(*calls, call{command, p})
		a := answers[command]
		return a.code, []byte(a.stdout), []byte(a.stderr), a.err
	}
}

type answer = struct {
	code   int
	stdout string
	stderr string
	err    error
}

var hooks = []Hook{
	{Name: "guard", Event: "PreToolUse", Matcher: "write", Command: "guard"},
	{Name: "git", Event: "PreToolUse", Matcher: "shell", Command: "git"},
	{Name: "any", Event: "PreToolUse", Command: "any"},
	{Name: "fmt", Event: "PostToolUse", Matcher: "write", Command: "fmt"},
	{Name: "prompt", Event: "UserPromptSubmit", Command: "prompt"},
	{Name: "start", Event: "SessionStart", Command: "start"},
	{Name: "stop", Event: "Stop", Command: "stop"},
}

func TestMatchersAndPayload(t *testing.T) {
	var calls []call
	r := OpenCode(Request{Hook: "tool.execute.before", Tool: "multiedit", SessionID: "s", Directory: "/repo", Args: map[string]any{"filePath": "A.cs", "oldString": "a"}}, hooks, fake(nil, &calls))
	if r.Block != "" || len(calls) != 2 || calls[0].command != "guard" || calls[1].command != "any" {
		t.Fatalf("multiedit is a write: %+v %+v", r, calls)
	}
	p := calls[0].payload
	input := p["tool_input"].(map[string]any)
	if p["hook_event_name"] != "PreToolUse" || p["tool_name"] != "MultiEdit" || input["file_path"] != "A.cs" || input["old_string"] != "a" || p["cwd"] != "/repo" || p["session_id"] != "s" {
		t.Errorf("payload: %v", p)
	}

	calls = nil
	OpenCode(Request{Hook: "tool.execute.before", Tool: "webfetch"}, hooks, fake(nil, &calls))
	if len(calls) != 1 || calls[0].command != "any" || calls[0].payload["tool_name"] != "webfetch" {
		t.Errorf("an unknown tool only matches hooks without a matcher: %+v", calls)
	}

	for hook, want := range map[string]string{"chat.message": "prompt", "session.created": "start", "session.idle": "stop"} {
		calls = nil
		OpenCode(Request{Hook: hook, Prompt: "hola"}, hooks, fake(nil, &calls))
		if len(calls) != 1 || calls[0].command != want {
			t.Errorf("%s ran %+v", hook, calls)
		}
	}
	if calls = nil; OpenCode(Request{Hook: "file.edited"}, hooks, fake(nil, &calls)).Block != "" || len(calls) != 0 {
		t.Error("unknown plugin hooks are ignored")
	}
}

func TestBlocking(t *testing.T) {
	cases := map[string]struct {
		hook   string
		answer answer
		want   string
	}{
		"exit 2 with reason":  {"tool.execute.before", answer{code: 2, stderr: "no .cs\n"}, "no .cs"},
		"exit 2 without text": {"tool.execute.before", answer{code: 2}, "blocked by hook guard"},
		"deny json":           {"tool.execute.before", answer{stdout: `{"hookSpecificOutput":{"permissionDecision":"deny","permissionDecisionReason":"nope"}}`}, "nope"},
		"block json":          {"tool.execute.before", answer{stdout: `{"decision":"block","reason":"stop"}`}, "stop"},
		"exit 1 never blocks": {"tool.execute.before", answer{code: 1, stderr: "crash"}, ""},
		"error never blocks":  {"tool.execute.before", answer{err: errors.New("timed out")}, ""},
		"allow json":          {"tool.execute.before", answer{stdout: `{"hookSpecificOutput":{"permissionDecision":"allow"}}`}, ""},
		"post tool cannot":    {"tool.execute.after", answer{code: 2, stderr: "x"}, ""},
		"prompt can block":    {"chat.message", answer{code: 2, stderr: "no"}, "no"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			var calls []call
			answers := map[string]answer{"guard": c.answer, "fmt": c.answer, "prompt": c.answer}
			r := OpenCode(Request{Hook: c.hook, Tool: "write"}, hooks, fake(answers, &calls))
			if r.Block != c.want {
				t.Errorf("block = %q, want %q", r.Block, c.want)
			}
		})
	}
	// The first blocking hook stops the rest.
	var calls []call
	OpenCode(Request{Hook: "tool.execute.before", Tool: "write"}, hooks, fake(map[string]answer{"guard": {code: 2}}, &calls))
	if len(calls) != 1 {
		t.Errorf("hooks after a block must not run: %+v", calls)
	}
}

func TestExec(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	code, stdout, stderr, err := Exec(`read x; echo "got $x"; echo oops >&2; exit 2`, []byte("in\n"), 5*time.Second)
	if err != nil || code != 2 || strings.TrimSpace(string(stdout)) != "got in" || strings.TrimSpace(string(stderr)) != "oops" {
		t.Errorf("code=%d stdout=%q stderr=%q err=%v", code, stdout, stderr, err)
	}
	start := time.Now()
	if _, _, _, err := Exec("sleep 5", nil, 200*time.Millisecond); err == nil || time.Since(start) > 3*time.Second {
		t.Errorf("timeout: err=%v after %s", err, time.Since(start))
	}
}

// A hook that exits 0 but leaves a background child holding stdout still has its deny read.
func TestExecHookWithBackgroundChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	code, stdout, _, err := Exec(`sleep 3 & echo '{"hookSpecificOutput":{"permissionDecision":"deny","permissionDecisionReason":"bg"}}'`, nil, 10*time.Second)
	if err != nil || code != 0 || blockReason("h", code, stdout, nil) != "bg" {
		t.Errorf("code=%d stdout=%q err=%v", code, stdout, err)
	}
}
