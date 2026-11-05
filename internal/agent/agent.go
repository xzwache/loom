// Package agent runs the loop that sends the conversation to the model,
// executes the tools it asks for, and repeats until the model is done or a
// budget runs out.
package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
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
	// MaxParallel caps how many Parallel tool calls (subagents) of one turn
	// run at once; 0 or 1 runs every call in turn.
	MaxParallel int
	Budget      Budget
	Hooks       *hook.Pipeline
	// Approve asks the user when the policy asks and no hook decided; nil
	// denies, as in headless mode. Subagents share it.
	Approve Approver
	Log     *slog.Logger
	// Compaction shrinks the history when it grows too big for the context
	// window; nil turns it off.
	Compaction *compact.Chain
	// RebuildSystem, if set, makes a new system prompt after a compaction
	// changed the history, such as one with the current memories. Changing
	// the prompt loses the cached prefix, which compaction has lost anyway.
	RebuildSystem func() string
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
	// System is the system prompt the run ended with: Agent.System, or the
	// one RebuildSystem made.
	System string
}

var errTimeBudget = errors.New("time budget exceeded")

type tokenStop struct {
	limit, used int
	parent      bool
}

// runState holds the state of one Run.
type runState struct {
	agent *Agent
	log   *slog.Logger
	tools *tool.Registry
	specs []llm.ToolSpec
	start time.Time
	// haltMu guards halted and haltReason: parallel tool calls fire hooks
	// that may halt the run.
	haltMu     sync.Mutex
	halted     bool
	haltReason string
	// stopHookActive is set once a Stop hook has sent the model back to work.
	stopHookActive bool
	subagents      usageMeter
	tokens         *tokenLedger
	// overTokens is the budget that stopped the run: its own or a parent's.
	overTokens tokenStop
	// contextSize is the context's size in tokens after the last response,
	// or after the last compaction, when the history had sizedAt messages.
	contextSize int
	sizedAt     int
	// compactedOnFull is set once the run has compacted because the context
	// was full; a second full context ends the run.
	compactedOnFull bool
}

func (a *Agent) Run(ctx context.Context, messages []llm.Message) (Result, error) {
	return a.RunAfter(ctx, messages, llm.Usage{})
}

// RunAfter is Run with tokens spent for this run before it started, such as
// a call that prepared the prompt: they are part of its Usage and count
// toward Budget.MaxTokens.
func (a *Agent) RunAfter(ctx context.Context, messages []llm.Message, spent llm.Usage) (Result, error) {
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
	rs.tokens = &tokenLedger{limit: a.Budget.MaxTokens, parent: ledgerFrom(ctx)}
	rs.tokens.add(spent.Total())
	ctx = withLedger(ctx, rs.tokens)
	if a.Budget.MaxDuration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeoutCause(ctx, a.Budget.MaxDuration, errTimeBudget)
		defer cancel()
	}

	res := Result{Messages: slices.Clone(messages), Usage: spent, System: a.System}
	err := rs.loop(ctx, &res)
	res.Usage = res.Usage.Add(rs.subagents.take())
	parent.add(res.Usage)
	switch {
	case res.Stop == StopHalted:
		res.Detail = rs.haltReason
	case res.Stop == StopMaxRunTokens && rs.overTokens.parent:
		res.Detail = fmt.Sprintf("the parent run's token budget (%d) is used up", rs.overTokens.limit)
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
		rs.fire(ctx, event.BudgetExceeded{Budget: "tokens", Limit: int64(rs.overTokens.limit), Used: int64(rs.overTokens.used)})
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

func (rs *runState) isHalted() bool {
	rs.haltMu.Lock()
	defer rs.haltMu.Unlock()
	return rs.halted
}

func (rs *runState) halt(reason string) {
	rs.haltMu.Lock()
	defer rs.haltMu.Unlock()
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
		last.Content = append(slices.Clone(last.Content), llm.SystemText(llm.KindHookContext, s))
		return
	}
	res.Messages = append(res.Messages, llm.Message{Role: llm.RoleUser, Content: []llm.Block{llm.SystemText(llm.KindHookContext, s)}})
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
