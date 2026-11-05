package frontend

import (
	"context"

	"github.com/xzwache/loom/internal/agent"
)

// Approver is a UI that can answer tool calls the permission policy asks
// about. Without one they are denied. Approve is called one at a time, without
// the update lock, so it may call State; it must return when ctx is done.
// Offer "allow always" only when Approval.Always is not empty.
type Approver interface {
	Approve(ctx context.Context, a agent.Approval) (agent.Answer, error)
}

func (s *Session) approve(ctx context.Context, a agent.Approval) (agent.Answer, error) {
	select {
	case s.asking <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-s.asking }()

	s.setToolStatus(a.Call.ToolUseID, StatusAwaitingApproval)
	defer s.setToolStatus(a.Call.ToolUseID, StatusPending)
	return s.approver.Approve(ctx, a)
}

func (s *Session) setToolStatus(id string, status ToolStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.state.setStatus(id, status); err != nil {
		s.log.Warn("frontend: approval does not fit the state", "error", err)
	}
}
