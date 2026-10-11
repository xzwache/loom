//go:build integration

package llm

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// Calls the real API: one tool round trip, including sending thinking blocks back.
func TestAnthropicToolRoundTrip(t *testing.T) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}
	model := os.Getenv("LOOM_MODEL")
	if model == "" {
		t.Skip("LOOM_MODEL not set")
	}
	m := NewAnthropic(AnthropicOptions{APIKey: key, Timeout: 2 * time.Minute, MaxRetries: 2})
	ctx := context.Background()

	req := Request{
		Model:     model,
		MaxTokens: 4096,
		System:    "Use the provided tools when they help.",
		Tools: []ToolSpec{{
			Name:        "secret_word",
			Description: "Returns today's secret word.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		}},
		Messages: []Message{{Role: RoleUser, Content: []Block{Text{Text: "What is today's secret word? Answer with just the word."}}}},
	}

	first, err := m.SendMessage(ctx, req)
	if err != nil {
		t.Fatalf("first SendMessage: %v", err)
	}
	if first.StopReason != StopToolUse {
		t.Fatalf("first stop reason = %s, want tool_use; content: %+v", first.StopReason, first.Content)
	}
	var results []Block
	for _, b := range first.Content {
		if tu, ok := b.(ToolUse); ok {
			results = append(results, ToolResult{ToolUseID: tu.ID, Content: "pumpernickel"})
		}
	}
	req.Messages = append(req.Messages,
		Message{Role: RoleAssistant, Content: first.Content},
		Message{Role: RoleUser, Content: results},
	)

	second, err := m.SendMessage(ctx, req)
	if err != nil {
		t.Fatalf("second SendMessage: %v", err)
	}
	if second.StopReason != StopEndTurn {
		t.Fatalf("second stop reason = %s, want end_turn", second.StopReason)
	}
	var text strings.Builder
	for _, b := range second.Content {
		if tx, ok := b.(Text); ok {
			text.WriteString(tx.Text)
		}
	}
	if !strings.Contains(strings.ToLower(text.String()), "pumpernickel") {
		t.Errorf("final answer = %q, want it to contain the tool result", text.String())
	}
	t.Logf("usage: first %+v, second %+v", first.Usage, second.Usage)
}
