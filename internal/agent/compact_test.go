package agent

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/xzwache/loom/internal/compact"
	"github.com/xzwache/loom/internal/event"
	"github.com/xzwache/loom/internal/hook"
	"github.com/xzwache/loom/internal/llm"
	"github.com/xzwache/loom/internal/llm/llmtest"
)

// summarized replaces any history with one message and reports the
// instructions it was given.
type summarized struct{ instructions *string }

func (summarized) Name() string { return "summarized" }

func (s summarized) Compact(_ context.Context, in compact.Input) (compact.Result, error) {
	if s.instructions != nil {
		*s.instructions = in.Instructions
	}
	summary := []llm.Message{{Role: llm.RoleUser, Content: []llm.Block{llm.Text{Text: "summary"}}}}
	return compact.Result{Messages: summary, Usage: llm.Usage{InputTokens: 5}}, nil
}

func sized(r llmtest.Reply, tokens int) llmtest.Reply {
	r.Response.Usage = llm.Usage{InputTokens: tokens}
	return r
}

func compacting(t *testing.T, model llm.Model, s compact.Strategy) *Agent {
	a := newAgent(t, model, echoTool())
	a.Compaction = &compact.Chain{Strategies: []compact.Strategy{s}, Trigger: 1000, Target: 500}
	a.Hooks = &hook.Pipeline{}
	return a
}

func TestRunCompactsBeforeTheContextIsFull(t *testing.T) {
	model := llmtest.NewReplay(
		sized(reply(llm.StopToolUse, use("t1", "echo")), 1200),
		reply(llm.StopEndTurn, llm.Text{Text: "done"}),
	)
	var instructions string
	a := compacting(t, model, summarized{instructions: &instructions})
	var names []event.Name
	recordNames(a.Hooks, &names)
	hook.On(a.Hooks, func(context.Context, event.PreCompact) (hook.Outcome, error) {
		return hook.Outcome{AdditionalContext: "keep the test names"}, nil
	})

	res, err := a.Run(context.Background(), prompt())
	if err != nil || res.Stop != StopDone {
		t.Fatalf("Run = %s, %v", res.Stop, err)
	}
	if msgs := model.Requests()[1].Messages; len(msgs) != 1 {
		t.Errorf("second request has %d messages, want only the summary", len(msgs))
	}
	if instructions != "keep the test names" {
		t.Errorf("instructions = %q, want the PreCompact context", instructions)
	}
	if !slices.Contains(names, event.PreCompact{}.Name()) || !slices.Contains(names, event.PostCompact{}.Name()) {
		t.Errorf("events = %v, want PreCompact and PostCompact", names)
	}
	if want := 1200 + 10 + 5; res.Usage.InputTokens != want {
		t.Errorf("input tokens = %d, want %d with the summary call", res.Usage.InputTokens, want)
	}
}

func TestRunCompactsWhenTheContextIsFull(t *testing.T) {
	model := llmtest.NewReplay(
		reply(llm.StopContextWindowExceeded, llm.Text{Text: "partial"}),
		reply(llm.StopEndTurn, llm.Text{Text: "done"}),
	)
	res, err := compacting(t, model, summarized{}).Run(context.Background(), prompt())
	if err != nil || res.Stop != StopDone {
		t.Fatalf("Run = %s, %v; want done after compacting", res.Stop, err)
	}
	checkHistory(t, res.Messages)
}

func TestRunStopsWhenTheContextIsFullTwice(t *testing.T) {
	model := llmtest.NewReplay(
		reply(llm.StopContextWindowExceeded),
		reply(llm.StopContextWindowExceeded),
	)
	res, err := compacting(t, model, summarized{}).Run(context.Background(), prompt())
	if err != nil || res.Stop != StopContextFull || res.Turns != 2 {
		t.Errorf("Run = %s after %d turns, %v; want %s after 2", res.Stop, res.Turns, err, StopContextFull)
	}
}

func TestRunPreCompactCanHalt(t *testing.T) {
	model := llmtest.NewReplay(reply(llm.StopContextWindowExceeded))
	a := compacting(t, model, summarized{})
	hook.On(a.Hooks, func(context.Context, event.PreCompact) (hook.Outcome, error) {
		return hook.Outcome{Halt: true, Reason: "no compaction here"}, nil
	})
	res, err := a.Run(context.Background(), prompt())
	if err != nil || res.Stop != StopHalted {
		t.Errorf("Run = %s, %v; want %s", res.Stop, err, StopHalted)
	}
}

func TestRunCountsNewToolOutputInTheContextSize(t *testing.T) {
	big := funcTool{name: "big", run: func(context.Context, json.RawMessage) (string, error) {
		return strings.Repeat("x", 4000), nil // about 1000 tokens
	}}
	model := llmtest.NewReplay(
		sized(reply(llm.StopToolUse, use("t1", "big")), 500),
		reply(llm.StopEndTurn, llm.Text{Text: "done"}),
	)
	a := newAgent(t, model, big)
	a.Compaction = &compact.Chain{Strategies: []compact.Strategy{summarized{}}, Trigger: 1000, Target: 500}

	if _, err := a.Run(context.Background(), prompt()); err != nil {
		t.Fatal(err)
	}
	if msgs := model.Requests()[1].Messages; len(msgs) != 1 {
		t.Errorf("second request has %d messages; the tool output should have triggered compaction", len(msgs))
	}
}

func TestRunRebuildsSystemAfterCompaction(t *testing.T) {
	model := llmtest.NewReplay(
		sized(reply(llm.StopToolUse, use("t1", "echo")), 1200),
		reply(llm.StopEndTurn, llm.Text{Text: "done"}),
	)
	a := compacting(t, model, summarized{})
	a.System = "old list"
	a.RebuildSystem = func() string { return "new list" }

	res, err := a.Run(context.Background(), prompt())
	if err != nil {
		t.Fatal(err)
	}
	reqs := model.Requests()
	if reqs[0].System != "old list" || reqs[1].System != "new list" || res.System != "new list" {
		t.Errorf("systems = %q, %q, result %q; want the rebuilt one after compaction", reqs[0].System, reqs[1].System, res.System)
	}
}
