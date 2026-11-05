package app

import (
	"context"
	"errors"

	"github.com/xzwache/loom/internal/agent"
	"github.com/xzwache/loom/internal/permission"
)

// newApprover wraps the UI's ask: it offers the session rules that "allow
// always" would add, and adds them when the user picks it. Nil without ask.
func newApprover(policy *permission.Policy, ask agent.Approver) agent.Approver {
	if ask == nil {
		return nil
	}
	return func(ctx context.Context, a agent.Approval) (agent.Answer, error) {
		a.Always = policy.SessionRules(a.Call.ToolName, a.Call.ToolInput)
		answer, err := ask(ctx, a)
		if err != nil || answer != agent.AllowAlways {
			return answer, err
		}
		if len(a.Always) == 0 {
			return "", errors.New("allow always was not offered for this call")
		}
		if err := policy.AllowForSession(a.Always); err != nil {
			return "", err
		}
		return answer, nil
	}
}
