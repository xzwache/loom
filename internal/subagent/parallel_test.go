package subagent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/xzwache/loom/internal/agent"
	"github.com/xzwache/loom/internal/hook"
	"github.com/xzwache/loom/internal/llm"
	"github.com/xzwache/loom/internal/tool"
)

// echoModel answers subagents with their own prompt, after a pause, and
// drives the parent: two agent calls, then done.
type echoModel struct {
	mu          sync.Mutex
	parentTurns int
	running     int
	peak        int
}

func (m *echoModel) SendMessage(_ context.Context, req llm.Request) (llm.Response, error) {
	usage := llm.Usage{InputTokens: 10, OutputTokens: 5}
	if strings.HasPrefix(req.System, "You are a loom subagent") {
		m.mu.Lock()
		m.running++
		m.peak = max(m.peak, m.running)
		m.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		m.mu.Lock()
		m.running--
		m.mu.Unlock()
		prompt := req.Messages[0].Content[0].(llm.Text).Text
		return llm.Response{Content: []llm.Block{llm.Text{Text: "report on " + prompt}}, StopReason: llm.StopEndTurn, Usage: usage}, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.parentTurns++
	if m.parentTurns == 1 {
		return llm.Response{Content: []llm.Block{agentCall("t1", "auth"), agentCall("t2", "billing")}, StopReason: llm.StopToolUse, Usage: usage}, nil
	}
	return llm.Response{Content: []llm.Block{llm.Text{Text: "done"}}, StopReason: llm.StopEndTurn, Usage: usage}, nil
}

func agentCall(id, prompt string) llm.ToolUse {
	in, _ := json.Marshal(map[string]string{"description": "look", "prompt": prompt})
	return llm.ToolUse{ID: id, Name: tool.Agent, Input: in}
}

func TestSubagentsRunInParallel(t *testing.T) {
	model := &echoModel{}
	hooks := &hook.Pipeline{}
	events := record(hooks)
	sub := newTool(t, agent.Agent{Model: model, ModelID: "m", Hooks: hooks, Budget: agent.Budget{MaxTurns: 5}})
	tools, err := tool.NewRegistry(sub)
	if err != nil {
		t.Fatal(err)
	}
	parent := agent.Agent{Model: model, ModelID: "m", Tools: tools, MaxParallel: 4, Hooks: hooks, Budget: agent.Budget{MaxTurns: 5}}

	res, err := parent.Run(context.Background(), []llm.Message{{Role: llm.RoleUser, Content: []llm.Block{llm.Text{Text: "go"}}}})
	if err != nil || res.Stop != agent.StopDone {
		t.Fatalf("Run = %s, %v", res.Stop, err)
	}
	if model.peak != 2 {
		t.Errorf("subagents running at once = %d, want 2", model.peak)
	}
	var got []string
	for _, b := range res.Messages[2].Content {
		r := b.(llm.ToolResult)
		got = append(got, r.ToolUseID+": "+r.Content)
	}
	if diff := cmp.Diff([]string{"t1: report on auth", "t2: report on billing"}, got); diff != "" {
		t.Errorf("results (-want +got):\n%s", diff)
	}
	// Two parent turns and two subagent turns, 15 tokens each.
	if total := res.Usage.Total(); total != 60 {
		t.Errorf("usage = %d tokens, want 60 with both subagents counted", total)
	}
	stops := 0
	for _, e := range events() {
		if e.name == "SubagentStop" {
			stops++
		}
	}
	if stops != 2 {
		t.Errorf("SubagentStop fired %d times, want 2", stops)
	}
}
