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

// tokenLedger counts a run's tokens as they are used, its subagents'
// included: every count also goes to the parent's ledger. A run checks its
// own limit and every parent's before each turn, so parallel subagents stop
// once the run that started them is out of tokens, not only when that run
// next checks.
type tokenLedger struct {
	mu     sync.Mutex
	used   int
	limit  int // 0: no limit
	parent *tokenLedger
}

type ledgerKey struct{}

func withLedger(ctx context.Context, l *tokenLedger) context.Context {
	return context.WithValue(ctx, ledgerKey{}, l)
}

func ledgerFrom(ctx context.Context) *tokenLedger {
	l, _ := ctx.Value(ledgerKey{}).(*tokenLedger)
	return l
}

func (l *tokenLedger) add(n int) {
	for ; l != nil; l = l.parent {
		l.mu.Lock()
		l.used += n
		l.mu.Unlock()
	}
}

// exhausted returns the nearest ledger, this one first, whose limit is
// reached, and what it has used.
func (l *tokenLedger) exhausted() (*tokenLedger, int, bool) {
	for ; l != nil; l = l.parent {
		l.mu.Lock()
		used := l.used
		l.mu.Unlock()
		if l.limit > 0 && used >= l.limit {
			return l, used, true
		}
	}
	return nil, 0, false
}
