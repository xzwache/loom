package hook

import (
	"context"

	"github.com/xzwache/loom/internal/event"
)

// Observer watches events: it runs after all hooks, sees the input they
// rewrote, and cannot change anything. It may be called from several
// goroutines at once.
type Observer func(ctx context.Context, e event.Event)

func (p *Pipeline) Observe(o Observer) { p.observers = append(p.observers, o) }

// Show gives an event to the observers only, never to hooks or the trace:
// for what only a UI needs, such as text as it streams.
func (p *Pipeline) Show(ctx context.Context, e event.Event) {
	if p == nil {
		return
	}
	p.observe(ctx, e)
}

func (p *Pipeline) observe(ctx context.Context, e event.Event) {
	for _, o := range p.observers {
		p.callObserver(ctx, o, e)
	}
}

// callObserver runs one observer. A panic in it is logged, so a broken UI
// cannot crash the run.
func (p *Pipeline) callObserver(ctx context.Context, o Observer, e event.Event) {
	defer func() {
		if r := recover(); r != nil {
			p.log().Warn("observer panicked", "event", e.Name(), "panic", r)
		}
	}()
	o(ctx, e)
}
