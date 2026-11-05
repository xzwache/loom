package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/xzwache/loom/internal/event"
	"github.com/xzwache/loom/internal/hook"
	"github.com/xzwache/loom/internal/llm"
	"github.com/xzwache/loom/internal/llm/llmtest"
	"github.com/xzwache/loom/internal/tool"
)

type parallelTool struct{ funcTool }

func (parallelTool) RunsInParallel() {}

// tracker records how many calls run at once and the order they end in.
type tracker struct {
	running, peak atomic.Int32
	mu            sync.Mutex
	ended         []string
}

func (tr *tracker) tool(name string, hold time.Duration) funcTool {
	return funcTool{name: name, run: func(_ context.Context, in json.RawMessage) (string, error) {
		n := tr.running.Add(1)
		for p := tr.peak.Load(); n > p && !tr.peak.CompareAndSwap(p, n); p = tr.peak.Load() {
		}
		time.Sleep(hold)
		tr.running.Add(-1)
		id := callID(in)
		tr.mu.Lock()
		tr.ended = append(tr.ended, id)
		tr.mu.Unlock()
		return "done " + id, nil
	}}
}

func callID(in json.RawMessage) string {
	var v struct{ ID string }
	_ = json.Unmarshal(in, &v)
	return v.ID
}

func call(id, name string) llm.ToolUse {
	return llm.ToolUse{ID: id, Name: name, Input: json.RawMessage(`{"id":"` + id + `"}`)}
}

// runCalls runs one turn that makes the given calls and returns their results
// as sent back to the model.
func runCalls(t *testing.T, a *Agent, model *llmtest.Replay) []llm.ToolResult {
	t.Helper()
	res, err := a.Run(context.Background(), prompt())
	if err != nil {
		t.Fatal(err)
	}
	checkHistory(t, res.Messages)
	return lastResults(t, model.Requests()[1].Messages)
}

func turnOf(uses ...llm.ToolUse) *llmtest.Replay {
	content := make([]llm.Block, len(uses))
	for i, u := range uses {
		content[i] = u
	}
	return llmtest.NewReplay(reply(llm.StopToolUse, content...), reply(llm.StopEndTurn, llm.Text{Text: "ok"}))
}

func resultIDs(results []llm.ToolResult) []string {
	ids := make([]string, len(results))
	for i, r := range results {
		ids[i] = r.ToolUseID + "=" + r.Content
	}
	return ids
}

func TestParallelCallsRunAtOnce(t *testing.T) {
	// Each call waits until all three have started; run in turn, they would
	// wait alone and time out.
	var started sync.WaitGroup
	started.Add(3)
	met := make(chan struct{})
	go func() { started.Wait(); close(met) }()
	barrier := parallelTool{funcTool{name: "agent", run: func(context.Context, json.RawMessage) (string, error) {
		started.Done()
		select {
		case <-met:
			return "met", nil
		case <-time.After(2 * time.Second):
			return "alone", nil
		}
	}}}
	model := turnOf(call("a", "agent"), call("b", "agent"), call("c", "agent"))
	a := newAgent(t, model, barrier)
	a.MaxParallel = 4

	got := runCalls(t, a, model)
	if diff := cmp.Diff([]string{"a=met", "b=met", "c=met"}, resultIDs(got)); diff != "" {
		t.Errorf("results (-want +got):\n%s", diff)
	}
}

func TestParallelCallsAreCapped(t *testing.T) {
	tr := &tracker{}
	model := turnOf(call("a", "agent"), call("b", "agent"), call("c", "agent"), call("d", "agent"))
	a := newAgent(t, model, parallelTool{tr.tool("agent", 50*time.Millisecond)})
	a.MaxParallel = 2

	got := runCalls(t, a, model)
	if p := tr.peak.Load(); p != 2 {
		t.Errorf("peak running = %d, want 2", p)
	}
	want := []string{"a=done a", "b=done b", "c=done c", "d=done d"}
	if diff := cmp.Diff(want, resultIDs(got)); diff != "" {
		t.Errorf("results keep the call order (-want +got):\n%s", diff)
	}
}

func TestOtherToolsRunInTurn(t *testing.T) {
	tr := &tracker{}
	model := turnOf(call("a", "agent"), call("b", "agent"), call("e", "echo"), call("c", "agent"))
	a := newAgent(t, model, parallelTool{tr.tool("agent", 30*time.Millisecond)}, tr.tool("echo", 0))
	a.MaxParallel = 4

	runCalls(t, a, model)
	// a and b run together, then echo alone, then c.
	if got := tr.ended[2:]; !cmp.Equal(got, []string{"e", "c"}) {
		t.Errorf("ended = %v, want a and b in any order, then e, then c", tr.ended)
	}
}

func TestNoParallelWithoutMaxParallel(t *testing.T) {
	tr := &tracker{}
	model := turnOf(call("a", "agent"), call("b", "agent"), call("c", "agent"))
	a := newAgent(t, model, parallelTool{tr.tool("agent", 10*time.Millisecond)})

	runCalls(t, a, model)
	if p := tr.peak.Load(); p != 1 {
		t.Errorf("peak running = %d, want 1 with MaxParallel unset", p)
	}
	if !cmp.Equal(tr.ended, []string{"a", "b", "c"}) {
		t.Errorf("ended = %v, want a, b, c", tr.ended)
	}
}

func TestHaltDuringParallelCalls(t *testing.T) {
	tr := &tracker{}
	model := turnOf(call("a", "agent"), call("b", "agent"), call("c", "agent"))
	a := newAgent(t, model, parallelTool{tr.tool("agent", 10*time.Millisecond)})
	a.MaxParallel = 4
	a.Hooks = &hook.Pipeline{}
	hook.On(a.Hooks, func(_ context.Context, e event.PreToolUse) (hook.Outcome, error) {
		if callID(e.ToolInput) == "b" {
			return hook.Outcome{Halt: true, Reason: "stop now"}, nil
		}
		return hook.Outcome{}, nil
	})

	res, err := a.Run(context.Background(), prompt())
	if err != nil {
		t.Fatal(err)
	}
	checkHistory(t, res.Messages)
	if res.Stop != StopHalted || res.Detail != "stop now" {
		t.Errorf("Stop = %s (%s), want halted (stop now)", res.Stop, res.Detail)
	}
}

var _ tool.Parallel = parallelTool{}

func busyReplies(n int) []llmtest.Reply {
	replies := make([]llmtest.Reply, n)
	for i := range replies {
		replies[i] = reply(llm.StopToolUse, use("n", "noop"))
	}
	return replies
}

func TestParentBudgetStopsSubagent(t *testing.T) {
	noop := funcTool{name: "noop", run: func(context.Context, json.RawMessage) (string, error) { return "ok", nil }}
	// The child has no token limit of its own; each turn costs 11 tokens.
	child := newAgent(t, llmtest.NewReplay(busyReplies(10)...), noop)
	var childRes Result
	spawn := funcTool{name: "spawn", run: func(ctx context.Context, _ json.RawMessage) (string, error) {
		res, err := child.Run(ctx, prompt())
		childRes = res
		return string(res.Stop), err
	}}
	parentModel := llmtest.NewReplay(reply(llm.StopToolUse, use("s", "spawn")), reply(llm.StopEndTurn, llm.Text{Text: "unreached"}))
	parent := newAgent(t, parentModel, spawn)
	parent.Budget.MaxTokens = 40

	res, err := parent.Run(context.Background(), prompt())
	if err != nil {
		t.Fatal(err)
	}
	// Parent 11, then the child 22, 33, 44 >= 40: the child stops after 3 turns.
	if childRes.Stop != StopMaxRunTokens || childRes.Turns != 3 || !strings.Contains(childRes.Detail, "parent run's token budget (40)") {
		t.Errorf("child = %s after %d turns (%s), want max_run_tokens after 3 for the parent's budget", childRes.Stop, childRes.Turns, childRes.Detail)
	}
	if res.Stop != StopMaxRunTokens || res.Detail != "" || res.Usage.Total() != 44 {
		t.Errorf("parent = %s (%q), %d tokens; want its own max_run_tokens with 44 tokens", res.Stop, res.Detail, res.Usage.Total())
	}
}
