package llmtest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/xzwache/loom/internal/llm"
)

func TestReplay(t *testing.T) {
	boom := errors.New("boom")
	s := NewReplay(
		ToolUseReply(llm.ToolUse{ID: "tu_1", Name: "bash"}),
		Reply{Err: boom},
		TextReply("done"),
	)
	ctx := context.Background()

	got, err := llm.Send(ctx, s, llm.Request{Model: "first"})
	if err != nil || got.StopReason != llm.StopToolUse {
		t.Fatalf("reply 1 = %+v, %v; want tool_use, nil", got, err)
	}
	if _, err := llm.Send(ctx, s, llm.Request{Model: "second"}); !errors.Is(err, boom) {
		t.Fatalf("reply 2 error = %v, want %v", err, boom)
	}
	got, err = llm.Send(ctx, s, llm.Request{Model: "third"})
	if err != nil || got.StopReason != llm.StopEndTurn {
		t.Fatalf("reply 3 = %+v, %v; want end_turn, nil", got, err)
	}
	if _, err := llm.Send(ctx, s, llm.Request{}); err == nil {
		t.Error("Send past the script: error = nil, want error")
	}

	var models []string
	for _, r := range s.Requests() {
		models = append(models, r.Model)
	}
	if diff := cmp.Diff([]string{"first", "second", "third", ""}, models); diff != "" {
		t.Errorf("recorded requests mismatch (-want +got):\n%s", diff)
	}
}

func TestReplayHonorsCancellation(t *testing.T) {
	s := NewReplay(TextReply("never"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := llm.Send(ctx, s, llm.Request{}); !errors.Is(err, context.Canceled) {
		t.Errorf("Send error = %v, want context.Canceled", err)
	}
	if s.Remaining() != 1 {
		t.Errorf("Remaining() = %d, want 1 (cancelled call must not consume a reply)", s.Remaining())
	}
}

func TestEventsRoundTrip(t *testing.T) {
	want := llm.Response{
		Content: []llm.Block{
			llm.Thinking{Thinking: "look at the files", Signature: "sig"},
			llm.RedactedThinking{Data: "opaque"},
			llm.ModelText("Listing the files now."),
			llm.ToolUse{ID: "tu_1", Name: "bash", Input: json.RawMessage(`{"command":"ls -la"}`)},
		},
		StopReason: llm.StopToolUse,
		Usage:      llm.Usage{InputTokens: 10, OutputTokens: 5},
	}
	evs := Events(want)
	var deltas int
	got, err := llm.Collect(Stream(evs...), func(ev llm.StreamEvent) {
		if _, ok := ev.(llm.TextDelta); ok {
			deltas++
		}
	})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("round trip mismatch (-want +got):\n%s", diff)
	}
	if deltas < 2 {
		t.Errorf("text came in %d deltas, want several", deltas)
	}
}
