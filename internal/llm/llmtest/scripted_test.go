package llmtest

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/xzwache/loom/internal/llm"
)

func TestScripted(t *testing.T) {
	boom := errors.New("boom")
	s := NewScripted(
		ToolUseReply(llm.ToolUse{ID: "tu_1", Name: "bash"}),
		Reply{Err: boom},
		TextReply("done"),
	)
	ctx := context.Background()

	got, err := s.SendMessage(ctx, llm.Request{Model: "first"})
	if err != nil || got.StopReason != llm.StopToolUse {
		t.Fatalf("reply 1 = %+v, %v; want tool_use, nil", got, err)
	}
	if _, err := s.SendMessage(ctx, llm.Request{Model: "second"}); !errors.Is(err, boom) {
		t.Fatalf("reply 2 error = %v, want %v", err, boom)
	}
	got, err = s.SendMessage(ctx, llm.Request{Model: "third"})
	if err != nil || got.StopReason != llm.StopEndTurn {
		t.Fatalf("reply 3 = %+v, %v; want end_turn, nil", got, err)
	}
	if _, err := s.SendMessage(ctx, llm.Request{}); err == nil {
		t.Error("SendMessage past the script: error = nil, want error")
	}

	var models []string
	for _, r := range s.Requests() {
		models = append(models, r.Model)
	}
	if diff := cmp.Diff([]string{"first", "second", "third", ""}, models); diff != "" {
		t.Errorf("recorded requests mismatch (-want +got):\n%s", diff)
	}
}

func TestScriptedHonorsCancellation(t *testing.T) {
	s := NewScripted(TextReply("never"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := s.SendMessage(ctx, llm.Request{}); !errors.Is(err, context.Canceled) {
		t.Errorf("SendMessage error = %v, want context.Canceled", err)
	}
	if s.Remaining() != 1 {
		t.Errorf("Remaining() = %d, want 1 (cancelled call must not consume a reply)", s.Remaining())
	}
}
