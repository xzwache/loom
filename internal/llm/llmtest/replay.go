// Package llmtest provides test doubles for llm.Model.
package llmtest

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"sync"

	"github.com/xzwache/loom/internal/llm"
)

type Reply struct {
	Response llm.Response
	Err      error
}

// Replay returns its replies in order and records every request it gets.
// It is safe for concurrent use.
type Replay struct {
	mu       sync.Mutex
	replies  []Reply
	requests []llm.Request
}

var _ llm.Model = (*Replay)(nil)

func NewReplay(replies ...Reply) *Replay {
	return &Replay{replies: replies}
}

// Stream sends the next reply as stream events (see Events), or its error.
func (s *Replay) Stream(ctx context.Context, req llm.Request) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		r, err := s.next(ctx, req)
		if err == nil {
			err = r.Err
		}
		if err != nil {
			yield(nil, err)
			return
		}
		for _, ev := range Events(r.Response) {
			if !yield(ev, nil) {
				return
			}
		}
	}
}

func (s *Replay) next(ctx context.Context, req llm.Request) (Reply, error) {
	if err := ctx.Err(); err != nil {
		return Reply{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, req)
	if len(s.replies) == 0 {
		return Reply{}, fmt.Errorf("llmtest: no reply left for request %d", len(s.requests))
	}
	r := s.replies[0]
	s.replies = s.replies[1:]
	return r, nil
}

func (s *Replay) Requests() []llm.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]llm.Request(nil), s.requests...)
}

func (s *Replay) Remaining() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.replies)
}

func TextReply(text string) Reply {
	return Reply{Response: llm.Response{
		Content:    []llm.Block{llm.ModelText(text)},
		StopReason: llm.StopEndTurn,
	}}
}

func ToolUseReply(uses ...llm.ToolUse) Reply {
	content := make([]llm.Block, len(uses))
	for i, u := range uses {
		content[i] = u
	}
	return Reply{Response: llm.Response{Content: content, StopReason: llm.StopToolUse}}
}

// Events turns a Response into the stream a provider would send for it:
// text, thinking and tool input in several deltas, so code that shows a
// stream sees more than one piece.
func Events(resp llm.Response) []llm.StreamEvent {
	var evs []llm.StreamEvent
	for i, b := range resp.Content {
		switch b := b.(type) {
		case llm.Text:
			evs = append(evs, llm.BlockStart{Index: i, Block: llm.Text{}})
			for _, part := range strings.SplitAfter(b.Text, " ") {
				evs = append(evs, llm.TextDelta{Index: i, Text: part})
			}
		case llm.Thinking:
			evs = append(evs, llm.BlockStart{Index: i, Block: llm.Thinking{}})
			for _, part := range strings.SplitAfter(b.Thinking, " ") {
				evs = append(evs, llm.ThinkingDelta{Index: i, Thinking: part})
			}
			evs = append(evs, llm.SignatureDelta{Index: i, Signature: b.Signature})
		case llm.ToolUse:
			evs = append(evs, llm.BlockStart{Index: i, Block: llm.ToolUse{ID: b.ID, Name: b.Name}})
			half := len(b.Input) / 2
			evs = append(evs, llm.ToolInputDelta{Index: i, JSON: string(b.Input[:half])},
				llm.ToolInputDelta{Index: i, JSON: string(b.Input[half:])})
		default:
			evs = append(evs, llm.BlockStart{Index: i, Block: b})
		}
		evs = append(evs, llm.BlockStop{Index: i})
	}
	return append(evs, llm.MessageStop{StopReason: resp.StopReason, Usage: resp.Usage})
}

// Stream yields evs in order, as a Model's Stream would.
func Stream(evs ...llm.StreamEvent) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		for _, ev := range evs {
			if !yield(ev, nil) {
				return
			}
		}
	}
}
