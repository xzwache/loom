// Package permission decides whether a tool call may run, from allow, ask and
// deny rules in the style of Claude Code. It runs as a PreToolUse hook.
//
// A Policy holds one ToolGuard per tool. A guard reads its tool's input and
// says what the call touches (a command, a file, or nothing in particular)
// and what kind of access it is; the policy then applies the rules and the
// mode in one fixed order (see evaluate), the same for every tool.
package permission

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"github.com/xzwache/loom/internal/event"
	"github.com/xzwache/loom/internal/hook"
	"github.com/xzwache/loom/internal/tool"
)

type Settings struct {
	Mode  Mode       `json:"mode"`
	Allow []string   `json:"allow"`
	Ask   []string   `json:"ask"`
	Deny  []string   `json:"deny"`
	Floor FloorRules `json:"floor"`
}

type Policy struct {
	mu     sync.RWMutex
	mode   Mode
	rules  RuleSet
	guards map[string]ToolGuard
}

// New builds a policy for a workspace rooted at dir. home expands "~/" in
// path rules; when empty, those rules never match.
func New(s Settings, dir, home string) (*Policy, error) {
	mode, modeErr := parseMode(s.Mode)
	rules, rulesErr := newRuleSet(s)
	floor, floorErr := newFloor(s.Floor)
	if err := errors.Join(modeErr, rulesErr, floorErr); err != nil {
		return nil, err
	}

	p := &Policy{mode: mode, rules: rules}
	ws := newWorkspace(dir, home)
	p.guards = map[string]ToolGuard{
		tool.Bash:  bashGuard{policy: p, floor: floor},
		tool.Read:  fileGuard{policy: p, ws: ws, tool: tool.Read, access: ReadOnly},
		tool.Edit:  fileGuard{policy: p, ws: ws, tool: tool.Edit, access: Edits},
		tool.Write: fileGuard{policy: p, ws: ws, tool: tool.Write, access: Edits},
		tool.Glob:  plainGuard{policy: p, tool: tool.Glob, access: ReadOnly},
		// These change only loom's own state. A subagent's own tool calls
		// are checked like any other.
		tool.Agent: plainGuard{policy: p, tool: tool.Agent, access: ReadOnly},
		tool.Skill: plainGuard{policy: p, tool: tool.Skill, access: ReadOnly},
		tool.Todo:  plainGuard{policy: p, tool: tool.Todo, access: ReadOnly},
	}
	return p, nil
}

func (s Settings) Validate() error {
	_, err := New(s, ".", "")
	return err
}

func (p *Policy) Mode() Mode {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.mode
}

// SetMode switches the mode for the calls that follow, as interactive mode
// does when the user changes it.
func (p *Policy) SetMode(m Mode) error {
	mode, err := parseMode(m)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.mode = mode
	return nil
}

func (p *Policy) Hook(_ context.Context, e event.PreToolUse) (hook.Outcome, error) {
	v := p.Decide(e.ToolName, e.ToolInput)
	return hook.Outcome{Decision: v.Decision, Reason: v.Reason}, nil
}

// Decide decides a call to the named tool. Tool names are case-insensitive;
// a tool without its own guard needs a rule.
func (p *Policy) Decide(name string, input json.RawMessage) Verdict {
	name = strings.ToLower(name)
	guard, ok := p.guards[name]
	if !ok {
		guard = plainGuard{policy: p, tool: name, access: Other}
	}
	return guard.Decide(input)
}

// evaluate applies the rules and the mode to one call, in this order:
//  1. a deny rule that fits some form of the target denies;
//  2. the mode's override for this access, if it has one;
//  3. an ask rule that fits some form asks;
//  4. an allow rule that fits every form allows, so a symlink cannot reach a
//     file the rule was not written for;
//  5. the mode's fallback for this access, or ask.
//
// The floor is not here: guards check it first, and nothing overrides it.
func (p *Policy) evaluate(tool string, access Access, t target) Verdict {
	spec := modes[p.Mode()]
	if r, ok := p.rules.deny.first(tool, t.fitsSome); ok {
		return denied("denied by rule " + r.raw)
	}
	if v, ok := spec.Override[access]; ok {
		return v
	}
	if r, ok := p.rules.ask.first(tool, t.fitsSome); ok {
		return asked("rule " + r.raw + " asks for approval")
	}
	if _, ok := p.rules.allow.first(tool, t.fitsEvery); ok {
		return allowed()
	}
	if v, ok := spec.Fallback[access]; ok {
		return v
	}
	return asked("no rule allows this " + tool + " call")
}
