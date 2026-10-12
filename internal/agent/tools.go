package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/xzwache/loom/internal/event"
	"github.com/xzwache/loom/internal/hook"
	"github.com/xzwache/loom/internal/llm"
	"github.com/xzwache/loom/internal/tool"
)

const logInputLimit = 200

// runTools answers every tool use in content, in order, in one user message.
func (rs *runState) runTools(ctx context.Context, content []llm.Block) llm.Message {
	var results []llm.Block
	var outcomes []event.ToolOutcome
	for _, b := range content {
		if use, ok := b.(llm.ToolUse); ok {
			res := rs.runTool(ctx, use)
			results = append(results, res)
			outcomes = append(outcomes, event.ToolOutcome{ToolUseID: use.ID, ToolName: use.Name, IsError: res.IsError})
		}
	}
	if out := rs.fire(ctx, event.PostToolBatch{Results: outcomes}); out.AdditionalContext != "" {
		results = append(results, llm.Text{Text: out.AdditionalContext})
	}
	return llm.Message{Role: llm.RoleUser, Content: results}
}

func (rs *runState) runTool(ctx context.Context, use llm.ToolUse) llm.ToolResult {
	if rs.halted {
		return failed(use.ID, "not run: a hook stopped the run")
	}
	if ctx.Err() != nil {
		return failed(use.ID, "not run: the run was stopped")
	}
	t, ok := rs.tools.Get(use.Name)
	if !ok {
		return failed(use.ID, fmt.Sprintf("unknown tool %q", use.Name))
	}

	call := event.ToolCall{ToolUseID: use.ID, ToolName: use.Name, ToolInput: use.Input}
	pre, err := rs.fireErr(ctx, event.PreToolUse{ToolCall: call})
	switch {
	case err != nil:
		rs.log.Warn("hook failed", "event", "PreToolUse", "error", err)
		return rs.deny(ctx, call, "a PreToolUse hook failed: "+err.Error())
	case rs.halted:
		return failed(use.ID, "not run: a hook stopped the run")
	case pre.Decision == hook.Deny:
		return rs.deny(ctx, call, pre.Reason)
	case pre.Decision == hook.Ask:
		req := rs.fire(ctx, event.PermissionRequest{ToolCall: call})
		switch {
		case rs.halted:
			return failed(use.ID, "not run: a hook stopped the run")
		case req.Decision == hook.Deny:
			return rs.deny(ctx, call, req.Reason)
		case req.Decision != hook.Allow:
			return rs.deny(ctx, call, "needs approval, and no one can approve it in this mode: "+pre.Reason)
		}
	}
	if pre.UpdatedInput != nil {
		call.ToolInput = pre.UpdatedInput
	}

	out, err := rs.exec(ctx, t, call)
	if err != nil {
		post := rs.fire(ctx, event.PostToolUseFailure{ToolCall: call, Error: err.Error()})
		return failed(use.ID, withContext(err.Error(), post.AdditionalContext))
	}
	post := rs.fire(ctx, event.PostToolUse{ToolCall: call, ToolResponse: out})
	if post.UpdatedOutput != nil {
		out = *post.UpdatedOutput
	}
	if post.Block {
		out = withContext(out, post.Reason)
	}
	return llm.ToolResult{ToolUseID: use.ID, Content: withContext(out, post.AdditionalContext)}
}

func (rs *runState) exec(ctx context.Context, t tool.Tool, call event.ToolCall) (out string, err error) {
	start := time.Now()
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("tool %s panicked: %v", call.ToolName, p)
		}
		rs.log.Info("tool", "name", call.ToolName, "input", tool.Truncate(string(call.ToolInput), logInputLimit),
			"error", err != nil, "duration", time.Since(start).Round(time.Millisecond))
	}()
	return t.Run(ctx, call.ToolInput)
}

func (rs *runState) deny(ctx context.Context, call event.ToolCall, reason string) llm.ToolResult {
	if reason == "" {
		reason = "blocked by a hook"
	}
	rs.fire(ctx, event.PermissionDenied{ToolCall: call, Reason: reason})
	return failed(call.ToolUseID, "denied: "+reason)
}

// skipTools answers tool uses that will not run, so the history stays valid.
func skipTools(res *Result, content []llm.Block, reason string) {
	var results []llm.Block
	for _, b := range content {
		if use, ok := b.(llm.ToolUse); ok {
			results = append(results, failed(use.ID, reason))
		}
	}
	if results != nil {
		res.Messages = append(res.Messages, llm.Message{Role: llm.RoleUser, Content: results})
	}
}

func failed(id, msg string) llm.ToolResult {
	return llm.ToolResult{ToolUseID: id, Content: msg, IsError: true}
}

func withContext(s, extra string) string {
	if extra == "" {
		return s
	}
	return s + "\n\n" + extra
}
