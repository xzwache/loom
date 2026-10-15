package agent

import (
	"context"
	"sync"

	"github.com/xzwache/loom/internal/llm"
)

// usageMeter collects the tokens that subagents use during one run, so they
// count in the run's budget and usage. A subagent finds its parent's meter in
// the context.
type usageMeter struct {
	mu    sync.Mutex
	usage llm.Usage
}

type meterKey struct{}

func withMeter(ctx context.Context, m *usageMeter) context.Context {
	return context.WithValue(ctx, meterKey{}, m)
}

// meterFrom returns the meter of the run that ctx belongs to, or nil.
func meterFrom(ctx context.Context) *usageMeter {
	m, _ := ctx.Value(meterKey{}).(*usageMeter)
	return m
}

// add is safe on a nil meter: a top-level run has no parent to report to.
func (m *usageMeter) add(u llm.Usage) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.usage = m.usage.Add(u)
}

// take returns the usage collected so far and resets the meter.
func (m *usageMeter) take() llm.Usage {
	m.mu.Lock()
	defer m.mu.Unlock()
	u := m.usage
	m.usage = llm.Usage{}
	return u
}
