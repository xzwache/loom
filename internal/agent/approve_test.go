package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/xzwache/loom/internal/event"
	"github.com/xzwache/loom/internal/hook"
	"github.com/xzwache/loom/internal/llm"
	"github.com/xzwache/loom/internal/llm/llmtest"
)

// askingAgent is an agent whose policy asks about every echo call.
func askingAgent(t *testing.T, model llm.Model, approve Approver) *Agent {
	t.Helper()
	a := newAgent(t, model, echoTool())
	a.Hooks = &hook.Pipeline{}
	onPre(a.Hooks, hook.Outcome{Decision: hook.Ask, Reason: "risky"}, nil)
	a.Approve = approve
	return a
}

func TestApproverAnswers(t *testing.T) {
	ran := llm.ToolResult{ToolUseID: "t1", Content: "echo {}"}
	tests := []struct {
		name   string
		answer Answer
		err    error
		want   llm.ToolResult
	}{
		{name: "allow once", answer: AllowOnce, want: ran},
		{name: "allow always", answer: AllowAlways, want: ran},
		{name: "deny", answer: Deny, want: llm.ToolResult{ToolUseID: "t1", Content: "denied: the user denied it", IsError: true}},
		{name: "unknown answer", answer: "maybe", want: llm.ToolResult{ToolUseID: "t1", Content: `denied: unknown approval answer "maybe"`, IsError: true}},
		{name: "approver fails", err: errors.New("ui gone"), want: llm.ToolResult{ToolUseID: "t1", Content: "denied: approval failed: ui gone", IsError: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := llmtest.NewReplay(reply(llm.StopToolUse, use("t1", "echo")), reply(llm.StopEndTurn))
			var asked []Approval
			a := askingAgent(t, model, func(_ context.Context, ap Approval) (Answer, error) {
				asked = append(asked, ap)
				return tt.answer, tt.err
			})

			if _, err := a.Run(context.Background(), prompt()); err != nil {
				t.Fatal(err)
			}
			want := []Approval{{Call: event.ToolCall{ToolUseID: "t1", ToolName: "echo", ToolInput: json.RawMessage(`{}`)}, Reason: "risky"}}
			if diff := cmp.Diff(want, asked); diff != "" {
				t.Errorf("approvals (-want +got):\n%s", diff)
			}
			if got := lastResults(t, model.Requests()[1].Messages); !cmp.Equal(got, []llm.ToolResult{tt.want}) {
				t.Errorf("tool results = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestApproverSeesTheRewrittenInput(t *testing.T) {
	model := llmtest.NewReplay(reply(llm.StopToolUse, use("t1", "echo")), reply(llm.StopEndTurn))
	var input json.RawMessage
	a := newAgent(t, model, echoTool())
	a.Hooks = &hook.Pipeline{}
	onPre(a.Hooks, hook.Outcome{Decision: hook.Ask, UpdatedInput: json.RawMessage(`{"safe":true}`)}, nil)
	a.Approve = func(_ context.Context, ap Approval) (Answer, error) {
		input = ap.Call.ToolInput
		return AllowOnce, nil
	}

	if _, err := a.Run(context.Background(), prompt()); err != nil {
		t.Fatal(err)
	}
	if string(input) != `{"safe":true}` {
		t.Errorf("approver saw input %s, want the rewritten one", input)
	}
}

func TestApproverNotAskedWhenAHookDecides(t *testing.T) {
	model := llmtest.NewReplay(reply(llm.StopToolUse, use("t1", "echo")), reply(llm.StopEndTurn))
	a := askingAgent(t, model, func(context.Context, Approval) (Answer, error) {
		t.Error("approver asked, but a PermissionRequest hook decided")
		return Deny, nil
	})
	hook.On(a.Hooks, func(context.Context, event.PermissionRequest) (hook.Outcome, error) {
		return hook.Outcome{Decision: hook.Allow}, nil
	})

	if _, err := a.Run(context.Background(), prompt()); err != nil {
		t.Fatal(err)
	}
}

func TestCancelWhileWaitingForApproval(t *testing.T) {
	model := llmtest.NewReplay(reply(llm.StopToolUse, use("t1", "echo")), reply(llm.StopEndTurn))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := askingAgent(t, model, func(ctx context.Context, _ Approval) (Answer, error) {
		cancel()
		<-ctx.Done()
		return "", ctx.Err()
	})
	var denied []string
	hook.On(a.Hooks, func(_ context.Context, e event.PermissionDenied) (hook.Outcome, error) {
		denied = append(denied, e.Reason)
		return hook.Outcome{}, nil
	})

	res, err := a.Run(ctx, prompt())
	if err != nil {
		t.Fatal(err)
	}
	if res.Stop != StopCancelled {
		t.Errorf("stop = %s, want cancelled", res.Stop)
	}
	if want := []string{"not approved: the run was stopped"}; !cmp.Equal(denied, want) {
		t.Errorf("PermissionDenied reasons = %v, want %v", denied, want)
	}
	checkHistory(t, res.Messages)
}
