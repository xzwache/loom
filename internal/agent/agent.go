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
	"time"

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
	Hooks     *hook.Chain
	Log       *slog.Logger
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
	Usage    llm.Usage
	// Detail explains a halted run.
	Detail string
}

var errTimeBudget = errors.New("time budget exceeded")

// run holds the state of one Run.
type run struct {
	agent      *Agent
	log        *slog.Logger
	tools      *tool.Registry
	specs      []llm.ToolSpec
	start      time.Time
	halted     bool
	haltReason string
}

// Run continues the conversation in messages, which must end with a user
// message. The returned history is always valid to send again: every tool
// use has a result. An error means a harness or provider fault; budget stops,
// hook halts and cancellation are reported through Result.Stop.
func (a *Agent) Run(ctx context.Context, messages []llm.Message) (Result, error) {
	if a.Model == nil {
		return Result{}, errors.New("agent has no model")
	}

	r := &run{agent: a, log: a.Log, tools: a.Tools, start: time.Now()}
	if r.tools == nil {
		r.tools, _ = tool.NewRegistry()
	}
	r.specs = r.tools.Specs()
	if r.log == nil {
		r.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if a.Budget.MaxDuration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeoutCause(ctx, a.Budget.MaxDuration, errTimeBudget)
		defer cancel()
	}

	res := Result{Messages: slices.Clone(messages)}
	err := r.loop(ctx, &res)
	if res.Stop == StopHalted {
		res.Detail = r.haltReason
	}
	r.finish(context.WithoutCancel(ctx), res, err)
	return res, err
}

func (r *run) loop(ctx context.Context, res *Result) error {
	a := r.agent
	if len(res.Messages) > 0 {
		out := r.fire(ctx, event.UserPromptSubmit{Prompt: text(res.Messages[len(res.Messages)-1].Content)})
		if out.Block || r.halted {
			r.halt(out.Reason)
			res.Stop = StopHalted
			return nil
		}
		appendUserText(res, out.AdditionalContext)
	}

	stopHookActive := false
	for {
		if res.Turns >= a.Budget.MaxTurns {
			res.Stop = StopMaxTurns
			return nil
		}
		if a.Budget.MaxTokens > 0 && res.Usage.Total() >= a.Budget.MaxTokens {
			res.Stop = StopMaxRunTokens
			return nil
		}

		r.fire(ctx, event.ModelRequest{Turn: res.Turns + 1, Model: a.ModelID, MessageCount: len(res.Messages), ToolCount: len(r.specs)})
		if r.halted {
			res.Stop = StopHalted
			return nil
		}
		resp, err := a.Model.SendMessage(ctx, llm.Request{
			Model:     a.ModelID,
			System:    a.System,
			Messages:  res.Messages,
			Tools:     r.specs,
			MaxTokens: a.MaxTokens,
		})
		if err != nil {
			if stop, ok := stopFromContext(ctx); ok {
				res.Stop = stop
				return nil
			}
			return fmt.Errorf("turn %d: %w", res.Turns+1, err)
		}
		res.Turns++
		res.Usage = res.Usage.Add(resp.Usage)
		r.log.Info("model", "turn", res.Turns, "stop", resp.StopReason,
			"input_tokens", resp.Usage.InputTokens, "output_tokens", resp.Usage.OutputTokens,
			"cache_read_tokens", resp.Usage.CacheReadTokens)
		if len(resp.Content) > 0 {
			res.Messages = append(res.Messages, llm.Message{Role: llm.RoleAssistant, Content: resp.Content})
		}
		r.fire(ctx, modelResponse(res.Turns, resp))
		if r.halted {
			skipTools(res, resp.Content, "not run: a hook stopped the run")
			res.Stop = StopHalted
			return nil
		}

		switch resp.StopReason {
		case llm.StopEndTurn, llm.StopSequence:
			res.Stop = StopDone
		case llm.StopPauseTurn:
			continue
		case llm.StopToolUse:
			res.Messages = append(res.Messages, r.runTools(ctx, resp.Content))
			if r.halted {
				res.Stop = StopHalted
				return nil
			}
			if stop, ok := stopFromContext(ctx); ok {
				res.Stop = stop
				return nil
			}
			continue
		case llm.StopMaxTokens:
			res.Stop = StopTruncated
		case llm.StopRefusal:
			res.Stop = StopRefusal
		case llm.StopContextWindowExceeded:
			res.Stop = StopContextFull
		default:
			return fmt.Errorf("turn %d: unhandled stop reason %q", res.Turns, resp.StopReason)
		}

		skipTools(res, resp.Content, "not run: the response ended with "+string(resp.StopReason))
		res.Text = text(resp.Content)
		if res.Stop == StopDone {
			out := r.fire(ctx, event.Stop{StopHookActive: stopHookActive, LastAssistantMessage: res.Text})
			if out.Block && !r.halted {
				stopHookActive = true
				reason := out.Reason
				if reason == "" {
					reason = "A Stop hook asked you to keep working."
				}
				appendUserText(res, reason)
				continue
			}
		}
		return nil
	}
}

// finish reports how the run ended. ctx must not be cancelled, so that
// hooks still run after a timeout or interrupt.
func (r *run) finish(ctx context.Context, res Result, err error) {
	b := r.agent.Budget
	switch res.Stop {
	case StopMaxTurns:
		r.fire(ctx, event.BudgetExceeded{Budget: "turns", Limit: int64(b.MaxTurns), Used: int64(res.Turns)})
	case StopMaxRunTokens:
		r.fire(ctx, event.BudgetExceeded{Budget: "tokens", Limit: int64(b.MaxTokens), Used: int64(res.Usage.Total())})
	case StopTimeout:
		r.fire(ctx, event.BudgetExceeded{Budget: "duration_ms", Limit: b.MaxDuration.Milliseconds(), Used: time.Since(r.start).Milliseconds()})
	case StopDone, StopCancelled, StopTruncated, StopRefusal, StopContextFull, StopHalted:
	}
	if err != nil {
		r.fire(ctx, event.StopFailure{Reason: "error", Error: err.Error()})
	} else if res.Stop != StopDone {
		r.fire(ctx, event.StopFailure{Reason: string(res.Stop), Error: res.Detail})
	}
}

// fire runs the hooks for e. A failing hook is logged and otherwise ignored.
func (r *run) fire(ctx context.Context, e event.Event) hook.Outcome {
	out, err := r.fireErr(ctx, e)
	if err != nil {
		r.log.Warn("hook failed", "event", e.Name(), "error", err)
	}
	return out
}

func (r *run) fireErr(ctx context.Context, e event.Event) (hook.Outcome, error) {
	out, err := r.agent.Hooks.Fire(ctx, e)
	if out.Halt {
		r.halt(out.Reason)
	}
	return out, err
}

func (r *run) halt(reason string) {
	if r.halted {
		return
	}
	r.halted = true
	r.haltReason = reason
	if reason == "" {
		r.haltReason = "stopped by a hook"
	}
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
