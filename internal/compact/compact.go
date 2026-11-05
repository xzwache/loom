// Package compact keeps a conversation inside the model's context window. A
// Chain runs strategies, cheapest first, until the history is small enough:
// first old tool output is offloaded to files, and only when that is not
// enough is the history replaced by a summary.
package compact

import (
	"context"
	"fmt"

	"github.com/xzwache/loom/internal/llm"
)

// Trigger is why a compaction runs.
type Trigger string

const (
	// Auto runs before a model call, when the context passes the threshold.
	Auto Trigger = "auto"
	// Reactive runs after the model reported that the context is full.
	Reactive Trigger = "reactive"
	// Manual runs when the user asks.
	Manual Trigger = "manual"
)

type Input struct {
	Messages     []llm.Message
	Instructions string
}

// Strategy makes a history smaller. It returns the history unchanged when it
// cannot.
type Strategy interface {
	Name() string
	Compact(ctx context.Context, in Input) (Result, error)
}

type Result struct {
	Messages []llm.Message
	// Usage is what the strategy itself used, such as a summary call.
	Usage   llm.Usage
	Failed  int
	FailErr error
}

// Chain runs strategies in order until the history fits.
type Chain struct {
	Strategies []Strategy
	// Trigger is the context size, in tokens, at which compaction starts.
	Trigger int
	// Target is the size to get under, below Trigger, so compaction does
	// not start again a few turns later.
	Target int
}

// Report is what one strategy did, for the trace.
type Report struct {
	Strategy     string
	TokensBefore int
	TokensAfter  int
	Usage        llm.Usage
	Failed       int
	FailErr      error
}

// Needed reports whether a context of size tokens should be compacted.
func (c Chain) Needed(size int) bool { return c.Trigger > 0 && size >= c.Trigger }

// Run compacts in.Messages, whose size the API last counted as size tokens.
// Sizes after a strategy are estimates (see Estimate). When a strategy fails,
// Run returns the history as the strategies before it left it.
func (c Chain) Run(ctx context.Context, in Input, size int) ([]llm.Message, []Report, error) {
	messages := in.Messages
	var reports []Report
	for _, strategy := range c.Strategies {
		if size <= c.Target {
			break
		}
		r, err := strategy.Compact(ctx, Input{Messages: messages, Instructions: in.Instructions})
		if err != nil {
			return messages, reports, fmt.Errorf("%s: %w", strategy.Name(), err)
		}
		removed := Estimate(messages) - Estimate(r.Messages)
		if removed == 0 && r.Usage == (llm.Usage{}) && r.Failed == 0 {
			continue // nothing to do, such as no old output to offload
		}
		after := max(0, size-removed)
		reports = append(reports, Report{
			Strategy:     strategy.Name(),
			TokensBefore: size,
			TokensAfter:  after,
			Usage:        r.Usage,
			Failed:       r.Failed,
			FailErr:      r.FailErr,
		})
		messages, size = r.Messages, after
	}
	return messages, reports, nil
}
