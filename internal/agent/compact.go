package agent

import (
	"context"

	"github.com/xzwache/loom/internal/compact"
	"github.com/xzwache/loom/internal/event"
	"github.com/xzwache/loom/internal/llm"
)

// compactOnFull compacts once after the model reported a full context, so the
// turn can be sent again. It drops the response that hit the limit: it is
// cut off, and its tool uses will not run.
func (rs *runState) compactOnFull(ctx context.Context, res *Result) bool {
	c := rs.agent.Compaction
	if c == nil || rs.compactedOnFull {
		return false
	}
	rs.compactedOnFull = true
	messages := res.Messages
	if n := len(messages); n > 0 && messages[n-1].Role == llm.RoleAssistant {
		messages = messages[:n-1]
	}
	return rs.compact(ctx, res, compact.Reactive, messages, max(rs.size(res), c.Trigger))
}

// compact runs the chain on messages, which take size tokens, and reports
// each strategy. A PreCompact hook can add instructions for a summary. It
// reports whether the history changed; a failed compaction is logged, and the
// run goes on with what it has.
func (rs *runState) compact(ctx context.Context, res *Result, trigger compact.Trigger, messages []llm.Message, size int) bool {
	pre := rs.fire(ctx, event.PreCompact{Trigger: string(trigger), Tokens: size})
	if rs.halted {
		res.Stop = StopHalted
		return false
	}
	out, reports, err := rs.agent.Compaction.Run(ctx, compact.Input{Messages: messages, Instructions: pre.AdditionalContext}, size)
	if err != nil {
		rs.log.Warn("compaction failed", "trigger", trigger, "error", err)
	}
	for _, r := range reports {
		res.Usage = res.Usage.Add(r.Usage)
		rs.report(ctx, trigger, r)
	}
	if len(reports) == 0 {
		return false
	}
	res.Messages = out
	rs.contextSize, rs.sizedAt = reports[len(reports)-1].TokensAfter, len(out)
	return true
}

// size is the context's size now: as last counted, plus an estimate of the
// messages added since, such as tool results. The API counts them only in
// the next response, which may be too late.
func (rs *runState) size(res *Result) int {
	if rs.sizedAt > len(res.Messages) {
		return rs.contextSize
	}
	return rs.contextSize + compact.Estimate(res.Messages[rs.sizedAt:])
}

func (rs *runState) report(ctx context.Context, trigger compact.Trigger, r compact.Report) {
	e := event.PostCompact{Trigger: string(trigger), Strategy: r.Strategy, TokensBefore: r.TokensBefore, TokensAfter: r.TokensAfter, Failed: r.Failed}
	rs.log.Info("compacted", "trigger", trigger, "strategy", r.Strategy, "tokens_before", r.TokensBefore, "tokens_after", r.TokensAfter)
	if r.FailErr != nil {
		e.Error = r.FailErr.Error()
		rs.log.Warn("compaction left parts as they were", "strategy", r.Strategy, "failed", r.Failed, "error", r.FailErr)
	}
	rs.fire(ctx, e)
}
