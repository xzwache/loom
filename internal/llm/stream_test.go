package llm_test

import (
	"encoding/json"
	"errors"
	"iter"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/xzwache/loom/internal/llm"
	"github.com/xzwache/loom/internal/llm/llmtest"
)

func TestCollectShowsEachEvent(t *testing.T) {
	evs := []llm.StreamEvent{
		llm.BlockStart{Index: 0, Block: llm.Text{}},
		llm.TextDelta{Index: 0, Text: "Hel"},
		llm.TextDelta{Index: 0, Text: "lo"},
		llm.BlockStop{Index: 0},
		llm.MessageStop{StopReason: llm.StopEndTurn, Usage: llm.Usage{OutputTokens: 2}},
	}
	var seen []llm.StreamEvent

	got, err := llm.Collect(llmtest.Stream(evs...), func(ev llm.StreamEvent) { seen = append(seen, ev) })
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	want := llm.Response{Content: []llm.Block{llm.ModelText("Hello")}, StopReason: llm.StopEndTurn, Usage: llm.Usage{OutputTokens: 2}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("response mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(evs, seen); diff != "" {
		t.Errorf("events shown mismatch (-want +got):\n%s", diff)
	}
}

func TestCollectBlocksOpenTogether(t *testing.T) {
	got, err := llm.Collect(llmtest.Stream(
		llm.BlockStart{Index: 0, Block: llm.ToolUse{ID: "a", Name: "read"}},
		llm.BlockStart{Index: 1, Block: llm.ToolUse{ID: "b", Name: "read"}},
		llm.ToolInputDelta{Index: 1, JSON: `{"path":"b"}`},
		llm.ToolInputDelta{Index: 0, JSON: `{"path":"a"}`},
		llm.BlockStop{Index: 1},
		llm.BlockStop{Index: 0},
		llm.MessageStop{StopReason: llm.StopToolUse},
	), nil)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	want := []llm.Block{
		llm.ToolUse{ID: "a", Name: "read", Input: json.RawMessage(`{"path":"a"}`)},
		llm.ToolUse{ID: "b", Name: "read", Input: json.RawMessage(`{"path":"b"}`)},
	}
	if diff := cmp.Diff(want, got.Content); diff != "" {
		t.Errorf("content mismatch (-want +got):\n%s", diff)
	}
}

func TestCollectToolWithoutInput(t *testing.T) {
	got, err := llm.Collect(llmtest.Stream(
		llm.BlockStart{Index: 0, Block: llm.ToolUse{ID: "a", Name: "glob"}},
		llm.BlockStop{Index: 0},
		llm.MessageStop{StopReason: llm.StopToolUse},
	), nil)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if use := got.Content[0].(llm.ToolUse); string(use.Input) != "{}" {
		t.Errorf("input = %s, want {}", use.Input)
	}
}

func TestCollectToolInputCutAtMaxTokens(t *testing.T) {
	got, err := llm.Collect(llmtest.Stream(
		llm.BlockStart{Index: 0, Block: llm.ToolUse{ID: "a", Name: "write"}},
		llm.ToolInputDelta{Index: 0, JSON: `{"path":"a","content":"half`},
		llm.BlockStop{Index: 0},
		llm.MessageStop{StopReason: llm.StopMaxTokens},
	), nil)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if use := got.Content[0].(llm.ToolUse); string(use.Input) != "{}" {
		t.Errorf("input = %s, want {}", use.Input)
	}
	if got.StopReason != llm.StopMaxTokens {
		t.Errorf("stop reason = %q, want max_tokens", got.StopReason)
	}
}

func TestCollectRejectsBadStreams(t *testing.T) {
	text := llm.BlockStart{Index: 0, Block: llm.Text{}}
	stop := llm.BlockStop{Index: 0}
	end := llm.MessageStop{StopReason: llm.StopEndTurn}
	tests := []struct {
		name string
		evs  []llm.StreamEvent
	}{
		{"delta before start", []llm.StreamEvent{llm.TextDelta{Index: 0, Text: "x"}, end}},
		{"start out of order", []llm.StreamEvent{llm.BlockStart{Index: 1, Block: llm.Text{}}}},
		{"start twice", []llm.StreamEvent{text, text}},
		{"block type that cannot stream", []llm.StreamEvent{llm.BlockStart{Index: 0, Block: llm.ToolResult{}}}},
		{"delta of the wrong kind", []llm.StreamEvent{text, llm.ToolInputDelta{Index: 0, JSON: "{}"}}},
		{"signature on text", []llm.StreamEvent{text, llm.SignatureDelta{Index: 0, Signature: "s"}}},
		{"delta after stop", []llm.StreamEvent{text, stop, llm.TextDelta{Index: 0, Text: "x"}}},
		{"stop twice", []llm.StreamEvent{text, stop, stop}},
		{"block open at message stop", []llm.StreamEvent{text, end}},
		{"event after message stop", []llm.StreamEvent{end, text}},
		{"unknown stop reason", []llm.StreamEvent{llm.MessageStop{StopReason: "weird"}}},
		{"no message stop", []llm.StreamEvent{text, stop}},
		{"empty stream", nil},
		{"tool input not JSON", []llm.StreamEvent{
			llm.BlockStart{Index: 0, Block: llm.ToolUse{ID: "a", Name: "bash"}},
			llm.ToolInputDelta{Index: 0, JSON: `{"command":`},
			llm.BlockStop{Index: 0},
			llm.MessageStop{StopReason: llm.StopToolUse},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := llm.Collect(llmtest.Stream(tt.evs...), nil)
			if !errors.Is(err, llm.ErrUnexpectedResponse) {
				t.Errorf("Collect error = %v, want ErrUnexpectedResponse", err)
			}
		})
	}
}

func TestCollectReturnsStreamError(t *testing.T) {
	boom := errors.New("connection reset")
	var stream iter.Seq2[llm.StreamEvent, error] = func(yield func(llm.StreamEvent, error) bool) {
		if !yield(llm.BlockStart{Index: 0, Block: llm.Text{}}, nil) {
			return
		}
		yield(nil, boom)
	}
	var shown int

	_, err := llm.Collect(stream, func(llm.StreamEvent) { shown++ })
	if !errors.Is(err, boom) {
		t.Errorf("Collect error = %v, want %v", err, boom)
	}
	if shown != 1 {
		t.Errorf("events shown = %d, want 1", shown)
	}
}
