// Package agent runs the loop that sends the conversation to the model,
// executes the tools it asks for, and repeats until the model is done or a
// budget runs out.
package agent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/xzwache/loom/internal/compact"
	"github.com/xzwache/loom/internal/event"
	"github.com/xzwache/loom/internal/hook"
	"github.com/xzwache/loom/internal/llm"
	"github.com/xzwache/loom/internal/tool"
)

type Agent struct {
	Model     llm.Model
	ModelID   string
	System    string
	Tools     *tool.Registry
	MaxTokens int
	Budget    Budget
	Hooks     *hook.Pipeline
	Log       *slog.Logger
	// Compaction shrinks the history when it grows too big for the context
	// window; nil turns it off.
	Compaction *compact.Chain
	// Subagent is set when this agent runs for another one. Its events then
	// carry the subagent in their context, it fires SubagentStop in place of
	// Stop, and it skips UserPromptSubmit.
	Subagent event.Agent
}

// Budget limits a single Run. Zero values mean no limit, except MaxTurns.
type Budget struct {
	MaxTurns    int
	MaxTokens   int
	MaxDuration time.Duration
}

type StopReason string

const (
	StopDone         StopReason = "done"
	StopMaxTurns     StopReason = "max_turns"
	StopMaxRunTokens StopReason = "max_run_tokens" //nolint:gosec // G101: a stop reason, not a credential
	StopTimeout      StopReason = "timeout"
	StopCancelled    StopReason = "cancelled"
	StopTruncated    StopReason = "truncated"
	StopRefusal      StopReason = "refusal"
	StopContextFull  StopReason = "context_full"
	StopHalted       StopReason = "halted"
)

type Result struct {
	Stop     StopReason
	Text     string
	Messages []llm.Message
	Turns    int
	// Usage includes the tokens of the subagents the run started.
	Usage llm.Usage
	// Detail explains a halted run.
	Detail string
}

var errTimeBudget = errors.New("time budget exceeded")

// runState holds the state of one Run.
type runState struct {
	agent      *Agent
	log        *slog.Logger
	tools      *tool.Registry
	specs      []llm.ToolSpec
	start      time.Time
	halted     bool
	haltReason string
	// stopHookActive is set once a Stop hook has sent the model back to work.
	stopHookActive bool
	subagents      usageMeter
	// contextSize is the context's size in tokens after the last response,
	// or after the last compaction, when the history had sizedAt messages.
	contextSize int
	sizedAt     int
	// compactedOnFull is set once the run has compacted because the context
	// was full; a second full context ends the run.
	compactedOnFull bool
}

func (a *Agent) Run(ctx context.Context, messages []llm.Message) (Result, error) {
	if a.Model == nil {
		return Result{}, errors.New("agent has no model")
	}

	rs := &runState{agent: a, log: a.Log, tools: a.Tools, start: time.Now()}
	if rs.tools == nil {
		rs.tools, _ = tool.NewRegistry()
	}
	rs.specs = rs.tools.Specs()
	if rs.log == nil {
		rs.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if a.Subagent.ID != "" {
		ctx = event.WithAgent(ctx, a.Subagent)
	}
	parent := meterFrom(ctx)
	ctx = withMeter(ctx, &rs.subagents)
	if a.Budget.MaxDuration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeoutCause(ctx, a.Budget.MaxDuration, errTimeBudget)
		defer cancel()
	}

	res := Result{Messages: slices.Clone(messages)}
	err := rs.loop(ctx, &res)
	res.Usage = res.Usage.Add(rs.subagents.take())
	parent.add(res.Usage)
	if res.Stop == StopHalted {
		res.Detail = rs.haltReason
	}
	rs.finish(context.WithoutCancel(ctx), res, err)
	return res, err
}

// finish reports how the run ended. ctx must not be cancelled, so that
// hooks still run after a timeout or interrupt.
func (rs *runState) finish(ctx context.Context, res Result, err error) {
	b := rs.agent.Budget
	switch res.Stop {
	case StopMaxTurns:
		rs.fire(ctx, event.BudgetExceeded{Budget: "turns", Limit: int64(b.MaxTurns), Used: int64(res.Turns)})
	case StopMaxRunTokens:
		rs.fire(ctx, event.BudgetExceeded{Budget: "tokens", Limit: int64(b.MaxTokens), Used: int64(res.Usage.Total())})
	case StopTimeout:
		rs.fire(ctx, event.BudgetExceeded{Budget: "duration_ms", Limit: b.MaxDuration.Milliseconds(), Used: time.Since(rs.start).Milliseconds()})
	case StopDone, StopCancelled, StopTruncated, StopRefusal, StopContextFull, StopHalted:
	}
	if err != nil {
		rs.fire(ctx, event.StopFailure{Reason: "error", Error: err.Error()})
	} else if res.Stop != StopDone {
		rs.fire(ctx, event.StopFailure{Reason: string(res.Stop), Error: res.Detail})
	}
}

// fire runs the hooks for e. A failing hook is logged and otherwise ignored.
func (rs *runState) fire(ctx context.Context, e event.Event) hook.Outcome {
	out, err := rs.fireErr(ctx, e)
	if err != nil {
		rs.log.Warn("hook failed", "event", e.Name(), "error", err)
	}
	return out
}

func (rs *runState) fireErr(ctx context.Context, e event.Event) (hook.Outcome, error) {
	out, err := rs.agent.Hooks.Fire(ctx, e)
	if out.Halt {
		rs.halt(out.Reason)
	}
	return out, err
}

func (rs *runState) halt(reason string) {
	if rs.halted {
		return
	}
	rs.halted = true
	rs.haltReason = reason
	if reason == "" {
		rs.haltReason = "stopped by a hook"
	}
}

func (rs *runState) stopEvent(active bool, last string) event.Event {
	if sub := rs.agent.Subagent; sub.ID != "" {
		return event.SubagentStop{AgentID: sub.ID, AgentType: sub.Type, StopHookActive: active, LastAssistantMessage: last}
	}
	return event.Stop{StopHookActive: active, LastAssistantMessage: last}
}

func modelResponse(turn int, resp llm.Response) event.ModelResponse {
	e := event.ModelResponse{Turn: turn, StopReason: resp.StopReason, Usage: resp.Usage, Text: text(resp.Content)}
	for _, b := range resp.Content {
		if u, ok := b.(llm.ToolUse); ok {
			e.ToolUses = append(e.ToolUses, event.ToolCall{ToolUseID: u.ID, ToolName: u.Name, ToolInput: u.Input})
		}
	}
	return e
}

// appendUserText adds text to the last message when it is the user's, so the
// history never has two user messages in a row.
func appendUserText(res *Result, s string) {
	if s == "" {
		return
	}
	n := len(res.Messages)
	if n > 0 && res.Messages[n-1].Role == llm.RoleUser {
		last := &res.Messages[n-1]
		last.Content = append(slices.Clone(last.Content), llm.Text{Text: s})
		return
	}
	res.Messages = append(res.Messages, llm.Message{Role: llm.RoleUser, Content: []llm.Block{llm.Text{Text: s}}})
}

func stopFromContext(ctx context.Context) (StopReason, bool) {
	if ctx.Err() == nil {
		return "", false
	}
	if errors.Is(context.Cause(ctx), errTimeBudget) {
		return StopTimeout, true
	}
	return StopCancelled, true
}

func text(content []llm.Block) string {
	var parts []string
	for _, b := range content {
		if t, ok := b.(llm.Text); ok {
			parts = append(parts, t.Text)
		}
	}
	return strings.Join(parts, "\n")
}
