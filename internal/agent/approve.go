package agent

import (
	"context"
	"fmt"

	"github.com/xzwache/loom/internal/event"
)

// Approver asks the user about a tool call the permission policy asks about
// and no PermissionRequest hook decided. It must return when ctx is done.
type Approver func(ctx context.Context, a Approval) (Answer, error)

type Approval struct {
	Call   event.ToolCall
	Agent  event.Agent // zero for the main agent
	Reason string      // why the policy asks
	// Always is the session rules AllowAlways adds; empty when it is not offered.
	Always []string
}

type Answer string

const (
	AllowOnce   Answer = "allow_once"
	AllowAlways Answer = "allow_always"
	Deny        Answer = "deny"
)

// approve asks the Approver about call. It returns why the call may not run,
// or "" when the user allowed it.
func (rs *runState) approve(ctx context.Context, call event.ToolCall, why string) string {
	if rs.agent.Approve == nil {
		return "needs approval, and no one can approve it in this mode: " + why
	}
	from, _ := event.AgentFrom(ctx)
	answer, err := rs.agent.Approve(ctx, Approval{Call: call, Agent: from, Reason: why})
	switch {
	case ctx.Err() != nil:
		return "not approved: the run was stopped"
	case err != nil:
		rs.log.Warn("approval failed", "tool", call.ToolName, "error", err)
		return "approval failed: " + err.Error()
	}
	rs.log.Info("approval", "tool", call.ToolName, "answer", answer)
	switch answer {
	case AllowOnce, AllowAlways:
		return ""
	case Deny:
		return "the user denied it"
	default:
		return fmt.Sprintf("unknown approval answer %q", answer)
	}
}
