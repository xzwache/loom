// Package llmtest provides test doubles for llm.Model.
package llmtest

import (
	"context"
	"fmt"
	"sync"

	"github.com/xzwache/loom/internal/llm"
)

type Reply struct {
	Response llm.Response
	Err      error
}

// Scripted returns its replies in order and records every request it gets.
// It is safe for concurrent use.
type Scripted struct {
	mu       sync.Mutex
	replies  []Reply
	requests []llm.Request
}

var _ llm.Model = (*Scripted)(nil)

func NewScripted(replies ...Reply) *Scripted {
	return &Scripted{replies: replies}
}

func (s *Scripted) SendMessage(ctx context.Context, req llm.Request) (llm.Response, error) {
	if err := ctx.Err(); err != nil {
		return llm.Response{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, req)
	if len(s.replies) == 0 {
		return llm.Response{}, fmt.Errorf("llmtest: no scripted reply for request %d", len(s.requests))
	}
	r := s.replies[0]
	s.replies = s.replies[1:]
	return r.Response, r.Err
}

func (s *Scripted) Requests() []llm.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]llm.Request(nil), s.requests...)
}

func (s *Scripted) Remaining() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.replies)
}

func TextReply(text string) Reply {
	return Reply{Response: llm.Response{
		Content:    []llm.Block{llm.Text{Text: text}},
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
