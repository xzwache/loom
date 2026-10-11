package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/xzwache/loom/internal/llm"
	"github.com/xzwache/loom/internal/llm/llmtest"
	"github.com/xzwache/loom/internal/tool"
)

type funcTool struct {
	name string
	run  func(ctx context.Context, input json.RawMessage) (string, error)
}

func (f funcTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{Name: f.name, InputSchema: json.RawMessage(`{"type":"object"}`)}
}

func (f funcTool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	return f.run(ctx, input)
}

func echoTool() funcTool {
	return funcTool{name: "echo", run: func(_ context.Context, in json.RawMessage) (string, error) {
		return "echo " + string(in), nil
	}}
}

func use(id, name string) llm.ToolUse {
	return llm.ToolUse{ID: id, Name: name, Input: json.RawMessage(`{}`)}
}

func prompt() []llm.Message {
	return []llm.Message{{Role: llm.RoleUser, Content: []llm.Block{llm.Text{Text: "go"}}}}
}

func reply(stop llm.StopReason, content ...llm.Block) llmtest.Reply {
	return llmtest.Reply{Response: llm.Response{Content: content, StopReason: stop, Usage: llm.Usage{InputTokens: 10, OutputTokens: 1}}}
}

func newAgent(model llm.Model, tools ...tool.Tool) *Agent {
	return &Agent{Model: model, ModelID: "test", MaxTokens: 100, Tools: tools, Budget: Budget{MaxTurns: 10}}
}

// checkHistory fails the test unless every tool use is answered by exactly
// one result in the next message, and no result is unmatched.
func checkHistory(t *testing.T, msgs []llm.Message) {
	t.Helper()
	for i, m := range msgs {
		var uses []string
		for _, b := range m.Content {
			if u, ok := b.(llm.ToolUse); ok {
				uses = append(uses, u.ID)
			}
		}
		var answered []string
		if i+1 < len(msgs) && msgs[i+1].Role == llm.RoleUser {
			for _, b := range msgs[i+1].Content {
				if r, ok := b.(llm.ToolResult); ok {
					answered = append(answered, r.ToolUseID)
				}
			}
		}
		if m.Role == llm.RoleAssistant && !cmp.Equal(uses, answered) {
			t.Errorf("message %d: tool uses %v answered by %v", i, uses, answered)
		}
	}
}

func lastResults(t *testing.T, msgs []llm.Message) []llm.ToolResult {
	t.Helper()
	var out []llm.ToolResult
	for _, b := range msgs[len(msgs)-1].Content {
		r, ok := b.(llm.ToolResult)
		if !ok {
			t.Fatalf("last message has %T, want only tool results", b)
		}
		out = append(out, r)
	}
	return out
}

func TestRunStopReasons(t *testing.T) {
	tests := []struct {
		name      string
		replies   []llmtest.Reply
		wantStop  StopReason
		wantText  string
		wantTurns int
	}{
		{
			name:      "end turn",
			replies:   []llmtest.Reply{reply(llm.StopEndTurn, llm.Text{Text: "hi"})},
			wantStop:  StopDone,
			wantText:  "hi",
			wantTurns: 1,
		},
		{
			name: "tool use then end turn",
			replies: []llmtest.Reply{
				reply(llm.StopToolUse, llm.Text{Text: "running"}, use("t1", "echo")),
				reply(llm.StopEndTurn, llm.Text{Text: "done"}),
			},
			wantStop:  StopDone,
			wantText:  "done",
			wantTurns: 2,
		},
		{
			name: "pause turn continues",
			replies: []llmtest.Reply{
				reply(llm.StopPauseTurn, llm.Text{Text: "thinking"}),
				reply(llm.StopEndTurn, llm.Text{Text: "done"}),
			},
			wantStop:  StopDone,
			wantText:  "done",
			wantTurns: 2,
		},
		{
			name:      "max tokens answers the cut-off tool use",
			replies:   []llmtest.Reply{reply(llm.StopMaxTokens, llm.Text{Text: "partial"}, use("t1", "echo"))},
			wantStop:  StopTruncated,
			wantText:  "partial",
			wantTurns: 1,
		},
		{
			name:      "refusal",
			replies:   []llmtest.Reply{reply(llm.StopRefusal)},
			wantStop:  StopRefusal,
			wantTurns: 1,
		},
		{
			name:      "context window exceeded",
			replies:   []llmtest.Reply{reply(llm.StopContextWindowExceeded)},
			wantStop:  StopContextFull,
			wantTurns: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := llmtest.NewScripted(tt.replies...)
			res, err := newAgent(model, echoTool()).Run(context.Background(), prompt())
			if err != nil {
				t.Fatalf("Run error: %v", err)
			}
			if res.Stop != tt.wantStop || res.Text != tt.wantText || res.Turns != tt.wantTurns {
				t.Errorf("Run = stop %q, text %q, turns %d; want %q, %q, %d",
					res.Stop, res.Text, res.Turns, tt.wantStop, tt.wantText, tt.wantTurns)
			}
			if model.Remaining() != 0 {
				t.Errorf("%d scripted replies unused", model.Remaining())
			}
			checkHistory(t, res.Messages)
		})
	}
}

func TestRunToolResults(t *testing.T) {
	failing := funcTool{name: "fail", run: func(context.Context, json.RawMessage) (string, error) {
		return "", errors.New("disk full")
	}}
	panicking := funcTool{name: "panic", run: func(context.Context, json.RawMessage) (string, error) {
		panic("bug")
	}}
	model := llmtest.NewScripted(
		reply(llm.StopToolUse, use("t1", "echo"), use("t2", "fail"), use("t3", "missing"), use("t4", "panic")),
		reply(llm.StopEndTurn, llm.Text{Text: "ok"}),
	)

	res, err := newAgent(model, echoTool(), failing, panicking).Run(context.Background(), prompt())
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	checkHistory(t, res.Messages)

	sent := model.Requests()[1].Messages
	got := lastResults(t, sent)
	want := []llm.ToolResult{
		{ToolUseID: "t1", Content: "echo {}"},
		{ToolUseID: "t2", Content: "disk full", IsError: true},
		{ToolUseID: "t3", Content: `unknown tool "missing"`, IsError: true},
		{ToolUseID: "t4", Content: "tool panic panicked: bug", IsError: true},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("tool results mismatch (-want +got):\n%s", diff)
	}
}

func TestRunSendsRequest(t *testing.T) {
	model := llmtest.NewScripted(reply(llm.StopEndTurn, llm.Text{Text: "hi"}))
	a := newAgent(model, echoTool())
	a.System = "be brief"

	if _, err := a.Run(context.Background(), prompt()); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	req := model.Requests()[0]
	if req.Model != "test" || req.System != "be brief" || req.MaxTokens != 100 {
		t.Errorf("request = model %q, system %q, max tokens %d", req.Model, req.System, req.MaxTokens)
	}
	if len(req.Tools) != 1 || req.Tools[0].Name != "echo" {
		t.Errorf("request tools = %+v, want [echo]", req.Tools)
	}
}

func TestRunDoesNotModifyInput(t *testing.T) {
	model := llmtest.NewScripted(reply(llm.StopEndTurn, llm.Text{Text: "hi"}))
	in := prompt()

	res, err := newAgent(model).Run(context.Background(), in)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if len(in) != 1 || len(res.Messages) != 2 {
		t.Errorf("input has %d messages, result %d; want 1 and 2", len(in), len(res.Messages))
	}
}

func TestRunBudgets(t *testing.T) {
	loop := func(n int) []llmtest.Reply {
		var r []llmtest.Reply
		for i := range n {
			r = append(r, reply(llm.StopToolUse, use("t"+string(rune('a'+i)), "echo")))
		}
		return r
	}

	t.Run("max turns", func(t *testing.T) {
		a := newAgent(llmtest.NewScripted(loop(5)...), echoTool())
		a.Budget.MaxTurns = 3
		res, err := a.Run(context.Background(), prompt())
		if err != nil || res.Stop != StopMaxTurns || res.Turns != 3 {
			t.Errorf("Run = %q after %d turns, err %v; want max_turns after 3", res.Stop, res.Turns, err)
		}
		checkHistory(t, res.Messages)
	})

	t.Run("max run tokens", func(t *testing.T) {
		a := newAgent(llmtest.NewScripted(loop(5)...), echoTool())
		a.Budget.MaxTokens = 20 // each reply uses 11
		res, err := a.Run(context.Background(), prompt())
		if err != nil || res.Stop != StopMaxRunTokens || res.Turns != 2 {
			t.Errorf("Run = %q after %d turns, err %v; want max_run_tokens after 2", res.Stop, res.Turns, err)
		}
		if res.Usage.Total() != 22 {
			t.Errorf("Usage.Total() = %d, want 22", res.Usage.Total())
		}
	})

	t.Run("max duration", func(t *testing.T) {
		slow := funcTool{name: "echo", run: func(ctx context.Context, _ json.RawMessage) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		}}
		a := newAgent(llmtest.NewScripted(loop(2)...), slow)
		a.Budget.MaxDuration = 50 * time.Millisecond
		res, err := a.Run(context.Background(), prompt())
		if err != nil || res.Stop != StopTimeout {
			t.Errorf("Run = %q, err %v; want timeout", res.Stop, err)
		}
		checkHistory(t, res.Messages)
	})
}

func TestRunCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	blocking := funcTool{name: "block", run: func(ctx context.Context, _ json.RawMessage) (string, error) {
		cancel()
		<-ctx.Done()
		return "", ctx.Err()
	}}
	model := llmtest.NewScripted(reply(llm.StopToolUse, use("t1", "block"), use("t2", "block")))

	res, err := newAgent(model, blocking).Run(ctx, prompt())
	if err != nil || res.Stop != StopCancelled {
		t.Fatalf("Run = %q, err %v; want cancelled", res.Stop, err)
	}
	checkHistory(t, res.Messages)
	got := lastResults(t, res.Messages)
	if len(got) != 2 || !got[1].IsError || !strings.Contains(got[1].Content, "not run") {
		t.Errorf("results after cancel = %+v, want t2 skipped", got)
	}
}

func TestRunErrors(t *testing.T) {
	boom := errors.New("boom")
	t.Run("model error", func(t *testing.T) {
		_, err := newAgent(llmtest.NewScripted(llmtest.Reply{Err: boom})).Run(context.Background(), prompt())
		if !errors.Is(err, boom) {
			t.Errorf("Run error = %v, want %v", err, boom)
		}
	})
	t.Run("duplicate tool", func(t *testing.T) {
		_, err := newAgent(llmtest.NewScripted(), echoTool(), echoTool()).Run(context.Background(), prompt())
		if err == nil || !strings.Contains(err.Error(), "duplicate tool") {
			t.Errorf("Run error = %v, want duplicate tool", err)
		}
	})
	t.Run("unknown stop reason", func(t *testing.T) {
		_, err := newAgent(llmtest.NewScripted(reply("new_reason"))).Run(context.Background(), prompt())
		if err == nil || !strings.Contains(err.Error(), "unhandled stop reason") {
			t.Errorf("Run error = %v, want unhandled stop reason", err)
		}
	})
}
