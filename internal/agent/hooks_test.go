package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/xzwache/loom/internal/event"
	"github.com/xzwache/loom/internal/hook"
	"github.com/xzwache/loom/internal/llm"
	"github.com/xzwache/loom/internal/llm/llmtest"
)

// recordNames registers a hook on c that appends every event name to names.
func recordNames(c *hook.Chain, names *[]event.Name) {
	c.OnAll(hook.Func(func(_ context.Context, e event.Event) (hook.Outcome, error) {
		*names = append(*names, e.Name())
		return hook.Outcome{}, nil
	}))
}

func onPre(c *hook.Chain, out hook.Outcome, err error) {
	hook.On(c, func(context.Context, event.PreToolUse) (hook.Outcome, error) { return out, err })
}

func TestRunFiresEvents(t *testing.T) {
	model := llmtest.NewScripted(
		reply(llm.StopToolUse, use("t1", "echo"), use("t2", "fail")),
		reply(llm.StopEndTurn, llm.Text{Text: "done"}),
	)
	failing := funcTool{name: "fail", run: func(context.Context, json.RawMessage) (string, error) {
		return "", errors.New("disk full")
	}}
	a := newAgent(t, model, echoTool(), failing)
	a.Hooks = &hook.Chain{}
	var names []event.Name
	recordNames(a.Hooks, &names)

	if _, err := a.Run(context.Background(), prompt()); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	want := []event.Name{
		"UserPromptSubmit",
		"ModelRequest", "ModelResponse",
		"PreToolUse", "PostToolUse", "PreToolUse", "PostToolUseFailure", "PostToolBatch",
		"ModelRequest", "ModelResponse",
		"Stop",
	}
	if diff := cmp.Diff(want, names); diff != "" {
		t.Errorf("events mismatch (-want +got):\n%s", diff)
	}
}

func TestRunPreToolUse(t *testing.T) {
	var gotInput string
	recording := funcTool{name: "echo", run: func(_ context.Context, in json.RawMessage) (string, error) {
		gotInput = string(in)
		return "ran", nil
	}}
	tests := []struct {
		name      string
		out       hook.Outcome
		err       error
		want      llm.ToolResult
		wantInput string
		wantEvent bool
	}{
		{name: "no decision", want: llm.ToolResult{ToolUseID: "t1", Content: "ran"}, wantInput: "{}"},
		{
			name:      "updated input",
			out:       hook.Outcome{Decision: hook.Allow, UpdatedInput: json.RawMessage(`{"x":1}`)},
			want:      llm.ToolResult{ToolUseID: "t1", Content: "ran"},
			wantInput: `{"x":1}`,
		},
		{
			name:      "deny",
			out:       hook.Outcome{Decision: hook.Deny, Reason: "not allowed"},
			want:      llm.ToolResult{ToolUseID: "t1", Content: "denied: not allowed", IsError: true},
			wantEvent: true,
		},
		{
			name:      "ask without a prompt",
			out:       hook.Outcome{Decision: hook.Ask, Reason: "risky"},
			want:      llm.ToolResult{ToolUseID: "t1", Content: "denied: needs approval, and no one can approve it in this mode: risky", IsError: true},
			wantEvent: true,
		},
		{
			name:      "failing hook denies",
			err:       errors.New("boom"),
			want:      llm.ToolResult{ToolUseID: "t1", Content: "denied: a PreToolUse hook failed: PreToolUse hook: boom", IsError: true},
			wantEvent: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotInput = ""
			model := llmtest.NewScripted(reply(llm.StopToolUse, use("t1", "echo")), reply(llm.StopEndTurn))
			a := newAgent(t, model, recording)
			a.Hooks = &hook.Chain{}
			onPre(a.Hooks, tt.out, tt.err)
			var denied []event.PermissionDenied
			hook.On(a.Hooks, func(_ context.Context, e event.PermissionDenied) (hook.Outcome, error) {
				denied = append(denied, e)
				return hook.Outcome{}, nil
			})

			if _, err := a.Run(context.Background(), prompt()); err != nil {
				t.Fatalf("Run error: %v", err)
			}
			got := lastResults(t, model.Requests()[1].Messages)
			if diff := cmp.Diff([]llm.ToolResult{tt.want}, got); diff != "" {
				t.Errorf("tool result mismatch (-want +got):\n%s", diff)
			}
			if gotInput != tt.wantInput {
				t.Errorf("tool ran with %q, want %q", gotInput, tt.wantInput)
			}
			if (len(denied) == 1) != tt.wantEvent {
				t.Errorf("PermissionDenied events = %+v, want fired: %v", denied, tt.wantEvent)
			}
		})
	}
}

func TestRunPostToolUse(t *testing.T) {
	model := llmtest.NewScripted(reply(llm.StopToolUse, use("t1", "echo")), reply(llm.StopEndTurn))
	a := newAgent(t, model, echoTool())
	a.Hooks = &hook.Chain{}
	hook.On(a.Hooks, func(_ context.Context, e event.PostToolUse) (hook.Outcome, error) {
		short := strings.ToUpper(e.ToolResponse)
		return hook.Outcome{UpdatedOutput: &short, AdditionalContext: "note"}, nil
	})

	if _, err := a.Run(context.Background(), prompt()); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	got := lastResults(t, model.Requests()[1].Messages)
	if want := []llm.ToolResult{{ToolUseID: "t1", Content: "ECHO {}\n\nnote"}}; !cmp.Equal(got, want) {
		t.Errorf("tool results = %+v, want %+v", got, want)
	}
}

func TestRunStopHookContinues(t *testing.T) {
	model := llmtest.NewScripted(reply(llm.StopEndTurn, llm.Text{Text: "first"}), reply(llm.StopEndTurn, llm.Text{Text: "second"}))
	a := newAgent(t, model)
	a.Hooks = &hook.Chain{}
	var active []bool
	hook.On(a.Hooks, func(_ context.Context, e event.Stop) (hook.Outcome, error) {
		active = append(active, e.StopHookActive)
		return hook.Outcome{Block: !e.StopHookActive, Reason: "run the tests"}, nil
	})

	res, err := a.Run(context.Background(), prompt())
	if err != nil || res.Stop != StopDone || res.Text != "second" || res.Turns != 2 {
		t.Fatalf("Run = %q, %q after %d turns, err %v; want done, second, 2", res.Stop, res.Text, res.Turns, err)
	}
	if !cmp.Equal(active, []bool{false, true}) {
		t.Errorf("stop_hook_active = %v, want [false true]", active)
	}
	sent := model.Requests()[1].Messages
	if last := sent[len(sent)-1]; last.Role != llm.RoleUser || text(last.Content) != "run the tests" {
		t.Errorf("second request ends with %+v, want the hook's reason", last)
	}
}

func TestRunUserPromptSubmit(t *testing.T) {
	t.Run("additional context", func(t *testing.T) {
		model := llmtest.NewScripted(reply(llm.StopEndTurn))
		a := newAgent(t, model)
		a.Hooks = &hook.Chain{}
		hook.On(a.Hooks, func(context.Context, event.UserPromptSubmit) (hook.Outcome, error) {
			return hook.Outcome{AdditionalContext: "branch: main"}, nil
		})
		in := prompt()

		if _, err := a.Run(context.Background(), in); err != nil {
			t.Fatalf("Run error: %v", err)
		}
		if got := text(model.Requests()[0].Messages[0].Content); got != "go\nbranch: main" {
			t.Errorf("sent prompt = %q, want the context appended", got)
		}
		if len(in[0].Content) != 1 {
			t.Error("Run modified the caller's prompt")
		}
	})

	t.Run("block", func(t *testing.T) {
		model := llmtest.NewScripted()
		a := newAgent(t, model)
		a.Hooks = &hook.Chain{}
		hook.On(a.Hooks, func(context.Context, event.UserPromptSubmit) (hook.Outcome, error) {
			return hook.Outcome{Block: true, Reason: "contains a secret"}, nil
		})

		res, err := a.Run(context.Background(), prompt())
		if err != nil || res.Stop != StopHalted || res.Detail != "contains a secret" || len(model.Requests()) != 0 {
			t.Errorf("Run = %q (%q), err %v, %d requests; want halted before any request",
				res.Stop, res.Detail, err, len(model.Requests()))
		}
	})
}

func TestRunHalt(t *testing.T) {
	model := llmtest.NewScripted(reply(llm.StopToolUse, use("t1", "echo"), use("t2", "echo")))
	a := newAgent(t, model, echoTool())
	a.Hooks = &hook.Chain{}
	onPre(a.Hooks, hook.Outcome{Halt: true, Reason: "out of disk"}, nil)
	var failures []event.StopFailure
	hook.On(a.Hooks, func(_ context.Context, e event.StopFailure) (hook.Outcome, error) {
		failures = append(failures, e)
		return hook.Outcome{}, nil
	})

	res, err := a.Run(context.Background(), prompt())
	if err != nil || res.Stop != StopHalted || res.Detail != "out of disk" {
		t.Fatalf("Run = %q (%q), err %v; want halted", res.Stop, res.Detail, err)
	}
	checkHistory(t, res.Messages)
	for _, r := range lastResults(t, res.Messages) {
		if !r.IsError || !strings.Contains(r.Content, "a hook stopped the run") {
			t.Errorf("result %+v, want skipped", r)
		}
	}
	if want := []event.StopFailure{{Reason: "halted", Error: "out of disk"}}; !cmp.Equal(failures, want) {
		t.Errorf("StopFailure events = %+v, want %+v", failures, want)
	}
}

func TestRunBudgetExceededEvent(t *testing.T) {
	model := llmtest.NewScripted(reply(llm.StopToolUse, use("t1", "echo")))
	a := newAgent(t, model, echoTool())
	a.Budget.MaxTurns = 1
	a.Hooks = &hook.Chain{}
	var got []event.BudgetExceeded
	hook.On(a.Hooks, func(_ context.Context, e event.BudgetExceeded) (hook.Outcome, error) {
		got = append(got, e)
		return hook.Outcome{}, nil
	})

	if _, err := a.Run(context.Background(), prompt()); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if want := []event.BudgetExceeded{{Budget: "turns", Limit: 1, Used: 1}}; !cmp.Equal(got, want) {
		t.Errorf("BudgetExceeded events = %+v, want %+v", got, want)
	}
}

func TestRunPermissionRequest(t *testing.T) {
	tests := []struct {
		name string
		out  hook.Outcome
		want llm.ToolResult
	}{
		{name: "approved", out: hook.Outcome{Decision: hook.Allow}, want: llm.ToolResult{ToolUseID: "t1", Content: "echo {}"}},
		{name: "rejected", out: hook.Outcome{Decision: hook.Deny, Reason: "no"}, want: llm.ToolResult{ToolUseID: "t1", Content: "denied: no", IsError: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := llmtest.NewScripted(reply(llm.StopToolUse, use("t1", "echo")), reply(llm.StopEndTurn))
			a := newAgent(t, model, echoTool())
			a.Hooks = &hook.Chain{}
			onPre(a.Hooks, hook.Outcome{Decision: hook.Ask, Reason: "risky"}, nil)
			hook.On(a.Hooks, func(context.Context, event.PermissionRequest) (hook.Outcome, error) { return tt.out, nil })

			if _, err := a.Run(context.Background(), prompt()); err != nil {
				t.Fatalf("Run error: %v", err)
			}
			if got := lastResults(t, model.Requests()[1].Messages); !cmp.Equal(got, []llm.ToolResult{tt.want}) {
				t.Errorf("tool results = %+v, want %+v", got, tt.want)
			}
		})
	}
}
