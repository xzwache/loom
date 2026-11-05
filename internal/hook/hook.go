// Package hook runs hooks on events in order and combines their outcomes.
package hook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
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

// Pipeline runs hooks in registration order, then the observers
// (observer.go). A nil Pipeline has no hooks.
type Pipeline struct {
	Log         *slog.Logger
	hookEntries []entry
	observers   []Observer
}

func (p *Pipeline) OnAll(h Hook) { p.hookEntries = append(p.hookEntries, entry{hook: h}) }
func (p *Pipeline) OnEvent(name event.Name, h Hook) {
	p.hookEntries = append(p.hookEntries, entry{name: name, hook: h})
}

func On[E event.Event](p *Pipeline, f func(ctx context.Context, e E) (Outcome, error)) {
	var zero E
	p.OnEvent(zero.Name(), Func(func(ctx context.Context, e event.Event) (Outcome, error) {
		return f(ctx, e.(E))
	}))
}

// Fire runs the hooks for e and merges their outcomes. Later hooks see the
// input or output rewritten by earlier ones. Deny and Halt stop the chain.
// A failing hook is skipped and its error is returned with the outcome of
// the others.
func (p *Pipeline) Fire(ctx context.Context, e event.Event) (Outcome, error) {
	var out Outcome
	if p == nil {
		return out, nil
	}
	var errs []error
	for _, hookEntry := range p.hookEntries {
		if hookEntry.name != "" && hookEntry.name != e.Name() {
			continue
		}
		outcome, err := handle(ctx, hookEntry.hook, e)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s hook: %w", e.Name(), err))
			continue
		}
		outcome = p.accepted(e.Name(), outcome)
		out = merge(out, outcome) // TODO: consider rework
		e = apply(e, outcome)
		if out.Decision == Deny || out.Halt {
			break
		}
	}
	p.observe(ctx, e)
	return out, errors.Join(errs...)
}

// Notify fires an event whose outcome the caller does not use. A failing hook
// is logged, never returned, so a broken script cannot stop loom.
func (p *Pipeline) Notify(ctx context.Context, e event.Event) {
	if _, err := p.Fire(ctx, e); err != nil {
		p.log().Warn("hook failed", "event", e.Name(), "error", err)
	}
}

func (p *Pipeline) log() *slog.Logger {
	if p == nil || p.Log == nil {
		return slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return p.Log
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
