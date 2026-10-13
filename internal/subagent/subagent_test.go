package subagent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/xzwache/loom/internal/agent"
	"github.com/xzwache/loom/internal/event"
	"github.com/xzwache/loom/internal/hook"
	"github.com/xzwache/loom/internal/llm"
	"github.com/xzwache/loom/internal/llm/llmtest"
)

type seen struct {
	name event.Name
	sub  event.Agent
}

func record(p *hook.Pipeline) func() []seen {
	var mu sync.Mutex
	var got []seen
	p.OnAll(hook.Func(func(ctx context.Context, e event.Event) (hook.Outcome, error) {
		sub, _ := event.AgentFrom(ctx)
		mu.Lock()
		defer mu.Unlock()
		got = append(got, seen{e.Name(), sub})
		return hook.Outcome{}, nil
	}))
	return func() []seen {
		mu.Lock()
		defer mu.Unlock()
		return got
	}
}

func run(t *testing.T, tl *Tool, prompt string) string {
	t.Helper()
	input, _ := json.Marshal(map[string]string{"description": "find it", "prompt": prompt})
	out, err := tl.Run(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRun(t *testing.T) {
	model := llmtest.NewScripted(llmtest.TextReply("found it in main.go:12"))
	hooks := &hook.Pipeline{}
	events := record(hooks)
	tl := &Tool{Template: agent.Agent{Model: model, ModelID: "cheap", System: "sub", Hooks: hooks, Budget: agent.Budget{MaxTurns: 5}}}

	if out := run(t, tl, "where is main?"); out != "found it in main.go:12" {
		t.Errorf("report = %q", out)
	}
	req := model.Requests()[0]
	if req.Model != "cheap" || req.System != "sub" || len(req.Messages) != 1 || req.Messages[0].Content[0].(llm.Text).Text != "where is main?" {
		t.Errorf("request = %+v, want a fresh history with the prompt", req)
	}

	got := events()
	var names []string
	for _, e := range got {
		names = append(names, string(e.name))
	}
	if want := "SubagentStart ModelRequest ModelResponse SubagentStop"; strings.Join(names, " ") != want {
		t.Errorf("events = %v, want %s", names, want)
	}
	id := got[0].sub.ID
	if id != "" {
		t.Errorf("SubagentStart fired inside the subagent: %+v", got[0])
	}
	for _, e := range got[1:] {
		if !strings.HasPrefix(e.sub.ID, "agent-") || e.sub.Type != TypeGeneral {
			t.Errorf("%s has agent %+v, want the subagent", e.name, e.sub)
		}
	}
}

func TestRunStoppedEarly(t *testing.T) {
	model := llmtest.NewScripted(
		llmtest.Reply{Response: llm.Response{
			Content:    []llm.Block{llm.Text{Text: "checking"}, llm.ToolUse{ID: "t1", Name: "missing", Input: json.RawMessage(`{}`)}},
			StopReason: llm.StopToolUse,
		}},
	)
	tl := &Tool{Template: agent.Agent{Model: model, Budget: agent.Budget{MaxTurns: 1}}}
	out := run(t, tl, "go")
	if !strings.Contains(out, "stopped before finishing (max_turns) after 1 turns") || !strings.Contains(out, "checking") {
		t.Errorf("report = %q", out)
	}
}

func TestRunRefuses(t *testing.T) {
	tl := &Tool{Template: agent.Agent{Model: llmtest.NewScripted()}}
	ctx := event.WithAgent(context.Background(), event.Agent{ID: "agent-1"})
	if _, err := tl.Run(ctx, json.RawMessage(`{"description":"x","prompt":"y"}`)); err == nil {
		t.Error("a subagent started a subagent")
	}
	if _, err := tl.Run(context.Background(), json.RawMessage(`{"description":"x","prompt":" "}`)); err == nil {
		t.Error("ran with an empty prompt")
	}
}
