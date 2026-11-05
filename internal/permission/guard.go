package permission

import (
	"encoding/json"
	"fmt"

	"github.com/xzwache/loom/internal/hook"
)

// ToolGuard decides whether calls to one tool may run.
type ToolGuard interface {
	Decide(input json.RawMessage) Verdict
}

// Verdict is the answer for one call: allow, ask or deny, and why.
type Verdict struct {
	Decision hook.Decision
	Reason   string
	// ruleCanAllow: no rule matched, so an allow rule would allow the call.
	ruleCanAllow bool
}

func allowed() Verdict             { return Verdict{Decision: hook.Allow} }
func asked(reason string) Verdict  { return Verdict{Decision: hook.Ask, Reason: reason} }
func denied(reason string) Verdict { return Verdict{Decision: hook.Deny, Reason: reason} }

var strictness = map[hook.Decision]int{hook.Allow: 0, hook.Ask: 1, hook.Deny: 2}

// stricterThan reports whether v is a stricter answer than o: deny, then
// ask, then allow.
func (v Verdict) stricterThan(o Verdict) bool { return strictness[v.Decision] > strictness[o.Decision] }

// plainGuard guards tools whose input rules do not look at: only a bare rule
// such as "Glob" applies to them.
type plainGuard struct {
	policy *Policy
	tool   string
	access Access
}

func (g plainGuard) Decide(json.RawMessage) Verdict {
	return g.policy.evaluate(g.tool, g.access, anyCall{})
}

// decodeInput reads a tool's JSON input into T.
func decodeInput[T any](input json.RawMessage) (T, error) {
	var in T
	if err := json.Unmarshal(input, &in); err != nil {
		return in, fmt.Errorf("cannot read the tool input: %w", err)
	}
	return in, nil
}
