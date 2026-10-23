package subagent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/xzwache/loom/internal/agent"
	"github.com/xzwache/loom/internal/llm"
	"github.com/xzwache/loom/internal/llm/llmtest"
	"github.com/xzwache/loom/internal/tool"
)

type noopTool struct{}

func (noopTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{Name: "noop", InputSchema: json.RawMessage(`{"type":"object"}`)}
}
func (noopTool) Run(context.Context, json.RawMessage) (string, error) { return "ok", nil }

// busyModel asks for a tool call forever, 15 tokens per turn.
func busyModel(turns int) *llmtest.Scripted {
	replies := make([]llmtest.Reply, turns)
	for i := range replies {
		replies[i] = llmtest.Reply{Response: llm.Response{
			Content:    []llm.Block{llm.ToolUse{ID: "t", Name: "noop", Input: json.RawMessage(`{}`)}},
			StopReason: llm.StopToolUse,
			Usage:      llm.Usage{InputTokens: 10, OutputTokens: 5},
		}}
	}
	return llmtest.NewScripted(replies...)
}

func TestMaxTokensInput(t *testing.T) {
	tests := []struct {
		name      string
		maxTokens int
		wantTurns int
	}{
		{"config limit", 0, 3},               // 15, 30, 45 >= 40
		{"lower request", 20, 2},             // 15, 30 >= 20
		{"request above the limit", 1000, 3}, // capped at 40
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := busyModel(10)
			tools, _ := tool.NewRegistry(noopTool{})
			tl := newTool(t, agent.Agent{Model: model, ModelID: "m", Tools: tools, Budget: agent.Budget{MaxTurns: 10, MaxTokens: 40}})
			in := map[string]any{"description": "d", "prompt": "p"}
			if tt.maxTokens > 0 {
				in["max_tokens"] = tt.maxTokens
			}
			input, _ := json.Marshal(in)
			out, err := tl.Run(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			if got := len(model.Requests()); got != tt.wantTurns || !strings.Contains(out, "max_run_tokens") {
				t.Errorf("turns = %d, report %q; want %d turns, stopped by max_run_tokens", got, out, tt.wantTurns)
			}
		})
	}
}

func TestMaxTokensRejectsNegative(t *testing.T) {
	tl := newTool(t, agent.Agent{Model: busyModel(1), ModelID: "m", Budget: agent.Budget{MaxTurns: 1}})
	if _, err := tl.Run(context.Background(), json.RawMessage(`{"prompt": "p", "max_tokens": -5}`)); err == nil {
		t.Error("Run with negative max_tokens: error = nil")
	}
}
