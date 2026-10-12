// Package hook runs hooks on events in order and combines their outcomes.
package hook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/xzwache/loom/internal/event"
)

type Decision string

const (
	Allow Decision = "allow"
	Ask   Decision = "ask"
	Deny  Decision = "deny"
)

// Outcome is what a hook asks for. Fields an event does not use are ignored.
type Outcome struct {
	Decision          Decision
	Reason            string
	UpdatedInput      json.RawMessage
	UpdatedOutput     *string
	AdditionalContext string
	// Block means "do not proceed": on Stop it keeps the run going, with
	// Reason given to the model.
	Block bool
	// Halt ends the whole run, with Reason as the explanation.
	Halt bool
}

type Hook interface {
	Handle(ctx context.Context, e event.Event) (Outcome, error)
}

type Func func(ctx context.Context, e event.Event) (Outcome, error)

func (f Func) Handle(ctx context.Context, e event.Event) (Outcome, error) { return f(ctx, e) }

type entry struct {
	name event.Name
	hook Hook
}

// Pipeline runs hooks in registration order. A nil Pipeline has no hooks.
type Pipeline struct {
	entries []entry
}

func (c *Pipeline) OnAll(h Hook) { c.entries = append(c.entries, entry{hook: h}) }
func (c *Pipeline) OnEvent(name event.Name, h Hook) {
	c.entries = append(c.entries, entry{name: name, hook: h})
}

func On[E event.Event](c *Pipeline, f func(ctx context.Context, e E) (Outcome, error)) {
	var zero E
	c.OnEvent(zero.Name(), Func(func(ctx context.Context, e event.Event) (Outcome, error) {
		return f(ctx, e.(E))
	}))
}

// Fire runs the hooks for e and merges their outcomes. Later hooks see the
// input or output rewritten by earlier ones. Deny and Halt stop the chain.
// A failing hook is skipped and its error is returned with the outcome of
// the others.
func (c *Pipeline) Fire(ctx context.Context, e event.Event) (Outcome, error) {
	var out Outcome
	if c == nil {
		return out, nil
	}
	var errs []error
	for _, en := range c.entries {
		if en.name != "" && en.name != e.Name() {
			continue
		}
		o, err := handle(ctx, en.hook, e)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s hook: %w", e.Name(), err))
			continue
		}
		out = merge(out, o)
		e = apply(e, o)
		if out.Decision == Deny || out.Halt {
			break
		}
	}
	return out, errors.Join(errs...)
}

func handle(ctx context.Context, h Hook, e event.Event) (out Outcome, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panicked: %v", r)
		}
	}()
	return h.Handle(ctx, e)
}

var rank = map[Decision]int{Allow: 1, Ask: 2, Deny: 3}

func merge(acc, o Outcome) Outcome {
	if rank[o.Decision] > rank[acc.Decision] {
		acc.Decision = o.Decision
	}
	acc.Reason = join(acc.Reason, o.Reason, "; ")
	if o.UpdatedInput != nil {
		acc.UpdatedInput = o.UpdatedInput
	}
	if o.UpdatedOutput != nil {
		acc.UpdatedOutput = o.UpdatedOutput
	}
	acc.AdditionalContext = join(acc.AdditionalContext, o.AdditionalContext, "\n")
	acc.Block = acc.Block || o.Block
	acc.Halt = acc.Halt || o.Halt
	return acc
}

func apply(e event.Event, o Outcome) event.Event {
	switch ev := e.(type) {
	case event.PreToolUse:
		if o.UpdatedInput != nil {
			ev.ToolInput = o.UpdatedInput
		}
		return ev
	case event.PostToolUse:
		if o.UpdatedOutput != nil {
			ev.ToolResponse = *o.UpdatedOutput
		}
		return ev
	default:
		return e
	}
}

func join(a, b, sep string) string {
	if a == "" || b == "" {
		return a + b
	}
	return strings.Join([]string{a, b}, sep)
}
