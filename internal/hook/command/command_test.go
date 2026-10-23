//go:build unix

package command

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/xzwache/loom/internal/event"
	"github.com/xzwache/loom/internal/hook"
)

func preBash(cmd string) event.PreToolUse {
	in, _ := json.Marshal(map[string]string{"command": cmd})
	return event.PreToolUse{ToolCall: event.ToolCall{ToolUseID: "t1", ToolName: "bash", ToolInput: in}}
}

func fire(t *testing.T, s Settings, e event.Event) (hook.Outcome, string) {
	t.Helper()
	dir := t.TempDir()
	c := &hook.Pipeline{}
	if err := Register(c, s, Env{SessionID: "run-1", Dir: dir, PermissionMode: "default"}); err != nil {
		t.Fatalf("Register error: %v", err)
	}
	out, err := c.Fire(context.Background(), e)
	if err != nil {
		t.Fatalf("Fire error: %v", err)
	}
	return out, dir
}

func one(event event.Name, matcher, command string) Settings {
	return Settings{event: {{Matcher: matcher, Hooks: []Handler{{Type: "command", Command: command}}}}}
}

func TestScriptInput(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-secret")
	_, dir := fire(t, one("PreToolUse", "Bash", `cat > input.json; env > env.txt`), preBash("ls"))

	data, err := os.ReadFile(filepath.Join(dir, "input.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"hook_event_name": "PreToolUse",
		"session_id":      "run-1",
		"cwd":             dir,
		"permission_mode": "default",
		"tool_use_id":     "t1",
		"tool_name":       "Bash",
		"tool_input":      map[string]any{"command": "ls"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("script input mismatch (-want +got):\n%s", diff)
	}
	env, err := os.ReadFile(filepath.Join(dir, "env.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(env), "sk-secret") || !strings.Contains(string(env), "CLAUDE_PROJECT_DIR="+dir) {
		t.Error("script environment leaks the API key or lacks CLAUDE_PROJECT_DIR")
	}
}

func TestScriptOutcomes(t *testing.T) {
	tests := []struct {
		name    string
		event   event.Event
		command string
		want    hook.Outcome
	}{
		{
			name:    "exit 2 denies a tool call",
			event:   preBash("rm x"),
			command: `echo "no deletes" >&2; exit 2`,
			want:    hook.Outcome{Decision: hook.Deny, Reason: "no deletes"},
		},
		{
			name:    "exit 2 blocks a stop",
			event:   event.Stop{},
			command: `echo "tests fail" >&2; exit 2`,
			want:    hook.Outcome{Block: true, Reason: "tests fail"},
		},
		{
			name:    "permission decision and updated input",
			event:   preBash("ls"),
			command: `echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"ask","permissionDecisionReason":"check","updatedInput":{"command":"ls -la"}}}'`,
			want:    hook.Outcome{Decision: hook.Ask, Reason: "check", UpdatedInput: json.RawMessage(`{"command":"ls -la"}`)},
		},
		{
			name:    "permission request",
			event:   event.PermissionRequest{ToolCall: event.ToolCall{ToolName: "bash"}},
			command: `echo '{"hookSpecificOutput":{"decision":{"behavior":"allow"}}}'`,
			want:    hook.Outcome{Decision: hook.Allow},
		},
		{
			name:    "block decision with context",
			event:   event.PostToolUse{ToolCall: event.ToolCall{ToolName: "edit"}},
			command: `echo '{"decision":"block","reason":"lint failed","hookSpecificOutput":{"additionalContext":"run gofmt"}}'`,
			want:    hook.Outcome{Block: true, Reason: "lint failed", AdditionalContext: "run gofmt"},
		},
		{
			name:    "continue false halts",
			event:   event.UserPromptSubmit{Prompt: "hi"},
			command: `echo '{"continue":false,"stopReason":"maintenance"}'`,
			want:    hook.Outcome{Halt: true, Reason: "maintenance"},
		},
		{
			name:    "plain text is prompt context",
			event:   event.UserPromptSubmit{Prompt: "hi"},
			command: `echo "branch: main"`,
			want:    hook.Outcome{AdditionalContext: "branch: main"},
		},
		{name: "plain text elsewhere is ignored", event: preBash("ls"), command: `echo hello`},
		{name: "other exit codes are ignored", event: preBash("ls"), command: `exit 1`},
		{name: "invalid JSON is ignored", event: preBash("ls"), command: `echo '{nope'`},
		{name: "matcher skips other tools", event: preBash("ls"), command: `exit 2`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matcher := ""
			if tt.name == "matcher skips other tools" {
				matcher = "Edit|Write"
			}
			got, _ := fire(t, one(tt.event.Name(), matcher, tt.command), tt.event)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("outcome mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestScriptTimeout(t *testing.T) {
	s := Settings{"PreToolUse": {{Hooks: []Handler{{Type: "command", Command: "sleep 5; exit 2", Timeout: 1}}}}}
	if got, _ := fire(t, s, preBash("ls")); !cmp.Equal(got, hook.Outcome{}) {
		t.Errorf("outcome after timeout = %+v, want none", got)
	}
}

func TestScriptTimeoutKillsChildren(t *testing.T) {
	s := Settings{"PreToolUse": {{Hooks: []Handler{{Type: "command", Command: `sleep 30 & echo $! > "$LOOM_PROJECT_DIR/pid"; wait`, Timeout: 1}}}}}
	_, dir := fire(t, s, preBash("ls"))
	data, err := os.ReadFile(filepath.Join(dir, "pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	for range 40 {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Errorf("background child %d still runs after the hook timed out", pid)
}

func TestScriptOutputTooLarge(t *testing.T) {
	s := one("PreToolUse", "", `head -c 2000000 /dev/zero | tr '\0' x >&2; exit 2`)
	if got, _ := fire(t, s, preBash("ls")); !cmp.Equal(got, hook.Outcome{}) {
		t.Errorf("outcome with oversized output = %+v, want none", got)
	}
}

func TestValidate(t *testing.T) {
	bad := Settings{
		"NoSuchEvent": {{Hooks: []Handler{{Type: "command", Command: "x"}}}},
		"Stop": {
			{Matcher: "(", Hooks: []Handler{{Type: "command", Command: "x"}}},
			{Hooks: []Handler{{Type: "prompt", Command: "x"}, {Type: "command"}, {Type: "command", Command: "x", Timeout: -1}}},
		},
	}
	err := bad.Validate()
	for _, want := range []string{"unknown event", "matcher", "unsupported hook type", "empty command", "negative timeout"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Validate error = %v, want it to mention %q", err, want)
		}
	}
}
