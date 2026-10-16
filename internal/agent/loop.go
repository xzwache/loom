package agent

import (
	"cmp"
	"context"
	"fmt"

	"github.com/xzwache/loom/internal/compact"
	"github.com/xzwache/loom/internal/event"
	"github.com/xzwache/loom/internal/llm"
)

// finalStops maps the model's stop reasons that end a run to the run's own.
var finalStops = map[llm.StopReason]StopReason{
	llm.StopEndTurn:               StopDone,
	llm.StopSequence:              StopDone,
	llm.StopMaxTokens:             StopTruncated,
	llm.StopRefusal:               StopRefusal,
	llm.StopContextWindowExceeded: StopContextFull,
}

// loop runs turns until one of them ends the run by setting res.Stop.
func (rs *runState) loop(ctx context.Context, res *Result) error {
	rs.submitPrompt(ctx, res)
	for res.Stop == "" {
		if err := rs.turn(ctx, res); err != nil {
			return err
		}
	}
	return nil
}

// submitPrompt lets UserPromptSubmit hooks block the prompt or add context.
// Subagents skip it: their prompt comes from the parent, not the user.
func (rs *runState) submitPrompt(ctx context.Context, res *Result) {
	if len(res.Messages) == 0 || rs.agent.Subagent.ID != "" {
		return
	}
	out := rs.fire(ctx, event.UserPromptSubmit{Prompt: text(res.Messages[len(res.Messages)-1].Content)})
	if out.Block || rs.halted {
		rs.halt(out.Reason)
		res.Stop = StopHalted
		return
	}
	appendUserText(res, out.AdditionalContext)
}

// turn sends the conversation to the model once and acts on the response.
func (rs *runState) turn(ctx context.Context, res *Result) error {
	if stop, over := rs.overBudget(*res); over {
		res.Stop = stop
		return nil
	}
	if c := rs.agent.Compaction; c != nil && c.Needed(rs.size(res)) {
		rs.compact(ctx, res, compact.Auto, res.Messages, rs.size(res))
		if res.Stop != "" {
			return nil
		}
	}
	resp, err := rs.send(ctx, res)
	if err != nil || res.Stop != "" {
		return err
	}
	rs.record(ctx, res, resp)
	if rs.halted {
		skipTools(res, resp.Content, "not run: a hook stopped the run")
		res.Stop = StopHalted
		return nil
	}
	return rs.handle(ctx, res, resp)
}

func (rs *runState) overBudget(res Result) (StopReason, bool) {
	b := rs.agent.Budget
	switch {
	case res.Turns >= b.MaxTurns:
		return StopMaxTurns, true
	case b.MaxTokens > 0 && res.Usage.Total() >= b.MaxTokens:
		return StopMaxRunTokens, true
	}
	return "", false
}

// send fires ModelRequest and sends the request. It sets res.Stop instead of
// returning an error when a hook halts the run or the context ends.
func (rs *runState) send(ctx context.Context, res *Result) (llm.Response, error) {
	a := rs.agent
	req := rs.fire(ctx, event.ModelRequest{Turn: res.Turns + 1, Model: a.ModelID, MessageCount: len(res.Messages), ToolCount: len(rs.specs)})
	if rs.halted {
		res.Stop = StopHalted
		return llm.Response{}, nil
	}
	// After pause_turn the history ends with the assistant, and the turn
	// must be resent as it is.
	if n := len(res.Messages); n > 0 && res.Messages[n-1].Role == llm.RoleUser {
		appendUserText(res, req.AdditionalContext)
	}
	resp, err := a.Model.SendMessage(ctx, llm.Request{
		Model:     a.ModelID,
		System:    a.System,
		Messages:  res.Messages,
		Tools:     rs.specs,
		MaxTokens: a.MaxTokens,
	})
	if err != nil {
		if stop, ok := stopFromContext(ctx); ok {
			res.Stop = stop
			return llm.Response{}, nil
		}
		return llm.Response{}, fmt.Errorf("turn %d: %w", res.Turns+1, err)
	}
	return resp, nil
}

// record adds the response to the history and the usage, and reports it.
func (rs *runState) record(ctx context.Context, res *Result, resp llm.Response) {
	res.Turns++
	res.Usage = res.Usage.Add(resp.Usage)
	rs.log.Info("model", "turn", res.Turns, "stop", resp.StopReason,
		"input_tokens", resp.Usage.InputTokens, "output_tokens", resp.Usage.OutputTokens,
		"cache_read_tokens", resp.Usage.CacheReadTokens)
	if len(resp.Content) > 0 {
		res.Messages = append(res.Messages, llm.Message{Role: llm.RoleAssistant, Content: resp.Content})
	}
	rs.contextSize, rs.sizedAt = compact.ContextSize(resp.Usage), len(res.Messages)
	rs.fire(ctx, modelResponse(res.Turns, resp))
}

// handle acts on why the model stopped.
func (rs *runState) handle(ctx context.Context, res *Result, resp llm.Response) error {
	if resp.StopReason == llm.StopPauseTurn {
		return nil
	}
	if resp.StopReason == llm.StopToolUse {
		rs.useTools(ctx, res, resp.Content)
		return nil
	}
	if resp.StopReason == llm.StopContextWindowExceeded {
		// Compacted: send the turn again. Halted by PreCompact: stop.
		if rs.compactOnFull(ctx, res) || res.Stop != "" {
			return nil
		}
	}
	stop, ok := finalStops[resp.StopReason]
	if !ok {
		return fmt.Errorf("turn %d: unhandled stop reason %q", res.Turns, resp.StopReason)
	}
	rs.end(ctx, res, resp, stop)
	return nil
}

// useTools runs the tools the model asked for and adds what the subagents
// among them used.
func (rs *runState) useTools(ctx context.Context, res *Result, content []llm.Block) {
	res.Messages = append(res.Messages, rs.runTools(ctx, content))
	res.Usage = res.Usage.Add(rs.subagents.take())
	if rs.halted {
		res.Stop = StopHalted
		return
	}
	if stop, ok := stopFromContext(ctx); ok {
		res.Stop = stop
	}
}

// end ends the run with stop, unless a Stop hook sends the model back to work.
func (rs *runState) end(ctx context.Context, res *Result, resp llm.Response, stop StopReason) {
	skipTools(res, resp.Content, "not run: the response ended with "+string(resp.StopReason))
	res.Text = text(resp.Content)
	if stop == StopDone && rs.keepWorking(ctx, res) {
		return
	}
	res.Stop = stop
}

// keepWorking fires Stop, or SubagentStop. A hook that blocks it sends its
// reason to the model, and the run goes on.
func (rs *runState) keepWorking(ctx context.Context, res *Result) bool {
	out := rs.fire(ctx, rs.stopEvent(rs.stopHookActive, res.Text))
	if !out.Block || rs.halted {
		return false
	}
	rs.stopHookActive = true
	appendUserText(res, cmp.Or(out.Reason, "A Stop hook asked you to keep working."))
	return true
}
