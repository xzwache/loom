package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/xzwache/loom/internal/event"
	"github.com/xzwache/loom/internal/hook"
	"github.com/xzwache/loom/internal/llm"
	"github.com/xzwache/loom/internal/llm/llmtest"
)

func TestRunShowsTheReplyAsItStreams(t *testing.T) {
	model := llmtest.NewReplay(
		reply(llm.StopToolUse, llm.Thinking{Thinking: "let me see", Signature: "sig"}, use("t1", "echo")),
		reply(llm.StopEndTurn, llm.Text{Text: "all done"}),
	)
	a := newAgent(t, model, echoTool())
	a.Hooks = &hook.Pipeline{}
	var shown []event.Event
	a.Hooks.Observe(func(_ context.Context, e event.Event) {
		switch e.(type) {
		case event.ModelTextDelta, event.ModelThinkingDelta, event.ModelToolUse, event.ToolRunning:
			shown = append(shown, e)
		}
	})

	if _, err := a.Run(context.Background(), prompt()); err != nil {
		t.Fatal(err)
	}
	want := []event.Event{
		event.ModelThinkingDelta{Thinking: "let "},
		event.ModelThinkingDelta{Thinking: "me "},
		event.ModelThinkingDelta{Thinking: "see"},
		event.ModelToolUse{ToolUseID: "t1", ToolName: "echo"},
		event.ToolRunning{ToolCall: event.ToolCall{ToolUseID: "t1", ToolName: "echo", ToolInput: json.RawMessage(`{}`)}},
		event.ModelTextDelta{Text: "all "},
		event.ModelTextDelta{Text: "done"},
	}
	if diff := cmp.Diff(want, shown); diff != "" {
		t.Errorf("shown events (-want +got):\n%s", diff)
	}
}

func TestDeniedToolIsNotShownRunning(t *testing.T) {
	model := llmtest.NewReplay(reply(llm.StopToolUse, use("t1", "echo")), reply(llm.StopEndTurn))
	a := newAgent(t, model, echoTool())
	a.Hooks = &hook.Pipeline{}
	hook.On(a.Hooks, func(context.Context, event.PreToolUse) (hook.Outcome, error) {
		return hook.Outcome{Decision: hook.Deny, Reason: "no"}, nil
	})
	running := 0
	a.Hooks.Observe(func(_ context.Context, e event.Event) {
		if _, ok := e.(event.ToolRunning); ok {
			running++
		}
	})

	if _, err := a.Run(context.Background(), prompt()); err != nil {
		t.Fatal(err)
	}
	if running != 0 {
		t.Errorf("ToolRunning shown %d times for a denied tool", running)
	}
}
