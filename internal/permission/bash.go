package permission

import (
	"encoding/json"
	"fmt"

	"github.com/xzwache/loom/internal/tool"
)

// bashGuard guards shell command lines. The floor comes first; then each
// command in the line (a && b | c) is decided on its own and the strictest
// answer wins.
type bashGuard struct {
	policy *Policy
	floor  *floor
}

func (g bashGuard) Decide(input json.RawMessage) Verdict {
	in, err := decodeInput[struct {
		Command string `json:"command"`
	}](input)
	if err != nil {
		return denied(err.Error())
	}
	cmds, err := parseCommands(in.Command)
	if err != nil {
		// A line that does not parse is judged as a whole: only bare Bash rules fit.
		return g.policy.evaluate(tool.Bash, Other, anyCall{})
	}
	if reason := g.floor.check(in.Command, cmds); reason != "" {
		return denied("never allowed: " + reason)
	}

	verdict := allowed()
	for _, cmd := range cmds {
		v := g.policy.evaluate(tool.Bash, cmd.access(), commandTarget{cmd.String()})
		if !v.stricterThan(verdict) {
			continue
		}
		verdict = v
		if v.Reason != "" && len(cmds) > 1 {
			verdict.Reason = fmt.Sprintf("%s (in %q)", v.Reason, cmd.String())
		}
	}
	return verdict
}

// access is ReadOnly for commands known to be safe, Other for the rest.
func (c command) access() Access {
	if c.safe() {
		return ReadOnly
	}
	return Other
}

// commandTarget is one command of a line; Bash(spec) rules match its text.
type commandTarget struct{ text string }

func (c commandTarget) fitsSome(r Rule) bool  { return r.matchCommand(c.text) }
func (c commandTarget) fitsEvery(r Rule) bool { return r.matchCommand(c.text) }
