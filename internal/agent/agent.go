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

	"github.com/xzwache/loom/internal/llm"
	"github.com/xzwache/loom/internal/tool"
)

type Agent struct {
	Model     llm.Model
	ModelID   string
	System    string
	Tools     []tool.Tool
	MaxTokens int
	Budget    Budget
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
)

type Result struct {
	Stop     StopReason
	Text     string
	Messages []llm.Message
	Turns    int
	Usage    llm.Usage
}

var errTimeBudget = errors.New("time budget exceeded")

// Run continues the conversation in messages, which must end with a user
// message. The returned history is always valid to send again: every tool
// use has a result. An error means a harness or provider fault; budget stops
// and cancellation are reported through Result.Stop.
func (a *Agent) Run(ctx context.Context, messages []llm.Message) (Result, error) {
	if a.Model == nil {
		return Result{}, errors.New("agent has no model")
	}
	tools, specs, err := indexTools(a.Tools)
	if err != nil {
		return Result{}, err
	}
	if a.Budget.MaxDuration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeoutCause(ctx, a.Budget.MaxDuration, errTimeBudget)
		defer cancel()
	}
	log := a.Log
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	res := Result{Messages: slices.Clone(messages)}
	for {
		if res.Turns >= a.Budget.MaxTurns {
			res.Stop = StopMaxTurns
			return res, nil
		}
		if a.Budget.MaxTokens > 0 && res.Usage.Total() >= a.Budget.MaxTokens {
			res.Stop = StopMaxRunTokens
			return res, nil
		}

		resp, err := a.Model.SendMessage(ctx, llm.Request{
			Model:     a.ModelID,
			System:    a.System,
			Messages:  res.Messages,
			Tools:     specs,
			MaxTokens: a.MaxTokens,
		})
		if err != nil {
			if stop, ok := stopFromContext(ctx); ok {
				res.Stop = stop
				return res, nil
			}
			return res, fmt.Errorf("turn %d: %w", res.Turns+1, err)
		}
		res.Turns++
		res.Usage = res.Usage.Add(resp.Usage)
		log.Info("model", "turn", res.Turns, "stop", resp.StopReason,
			"input_tokens", resp.Usage.InputTokens, "output_tokens", resp.Usage.OutputTokens,
			"cache_read_tokens", resp.Usage.CacheReadTokens)
		if len(resp.Content) > 0 {
			res.Messages = append(res.Messages, llm.Message{Role: llm.RoleAssistant, Content: resp.Content})
		}

		switch resp.StopReason {
		case llm.StopEndTurn, llm.StopSequence:
			res.Stop = StopDone
		case llm.StopPauseTurn:
			continue
		case llm.StopToolUse:
			res.Messages = append(res.Messages, runTools(ctx, log, tools, resp.Content))
			if stop, ok := stopFromContext(ctx); ok {
				res.Stop = stop
				return res, nil
			}
			continue
		case llm.StopMaxTokens:
			res.Stop = StopTruncated
		case llm.StopRefusal:
			res.Stop = StopRefusal
		case llm.StopContextWindowExceeded:
			res.Stop = StopContextFull
		default:
			return res, fmt.Errorf("turn %d: unhandled stop reason %q", res.Turns, resp.StopReason)
		}

		if unanswered := skipTools(resp.Content, "not run: the response ended with "+string(resp.StopReason)); unanswered != nil {
			res.Messages = append(res.Messages, *unanswered)
		}
		res.Text = text(resp.Content)
		return res, nil
	}
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
