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
func (r *run) runTools(ctx context.Context, content []llm.Block) llm.Message {
	var results []llm.Block
	var outcomes []event.ToolOutcome
	for _, b := range content {
		if use, ok := b.(llm.ToolUse); ok {
			res := r.runTool(ctx, use)
			results = append(results, res)
			outcomes = append(outcomes, event.ToolOutcome{ToolUseID: use.ID, ToolName: use.Name, IsError: res.IsError})
		}
	}
	if out := r.fire(ctx, event.PostToolBatch{Results: outcomes}); out.AdditionalContext != "" {
		results = append(results, llm.Text{Text: out.AdditionalContext})
	}
	return llm.Message{Role: llm.RoleUser, Content: results}
}

func (r *run) runTool(ctx context.Context, use llm.ToolUse) llm.ToolResult {
	if r.halted {
		return failed(use.ID, "not run: a hook stopped the run")
	}
	if ctx.Err() != nil {
		return failed(use.ID, "not run: the run was stopped")
	}
	t, ok := r.tools.Get(use.Name)
	if !ok {
		return failed(use.ID, fmt.Sprintf("unknown tool %q", use.Name))
	}

	call := event.ToolCall{ToolUseID: use.ID, ToolName: use.Name, ToolInput: use.Input}
	pre, err := r.fireErr(ctx, event.PreToolUse{ToolCall: call})
	switch {
	case err != nil:
		r.log.Warn("hook failed", "event", "PreToolUse", "error", err)
		return r.deny(ctx, call, "a PreToolUse hook failed: "+err.Error())
	case r.halted:
		return failed(use.ID, "not run: a hook stopped the run")
	case pre.Decision == hook.Deny:
		return r.deny(ctx, call, pre.Reason)
	case pre.Decision == hook.Ask:
		req := r.fire(ctx, event.PermissionRequest{ToolCall: call})
		switch {
		case r.halted:
			return failed(use.ID, "not run: a hook stopped the run")
		case req.Decision == hook.Deny:
			return r.deny(ctx, call, req.Reason)
		case req.Decision != hook.Allow:
			return r.deny(ctx, call, "needs approval, and no one can approve it in this mode: "+pre.Reason)
		}
	}
	if pre.UpdatedInput != nil {
		call.ToolInput = pre.UpdatedInput
	}

	out, err := r.exec(ctx, t, call)
	if err != nil {
		post := r.fire(ctx, event.PostToolUseFailure{ToolCall: call, Error: err.Error()})
		return failed(use.ID, withContext(err.Error(), post.AdditionalContext))
	}
	post := r.fire(ctx, event.PostToolUse{ToolCall: call, ToolResponse: out})
	if post.UpdatedOutput != nil {
		out = *post.UpdatedOutput
	}
	if post.Block {
		out = withContext(out, post.Reason)
	}
	return llm.ToolResult{ToolUseID: use.ID, Content: withContext(out, post.AdditionalContext)}
}

func (r *run) exec(ctx context.Context, t tool.Tool, call event.ToolCall) (out string, err error) {
	start := time.Now()
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("tool %s panicked: %v", call.ToolName, p)
		}
		r.log.Info("tool", "name", call.ToolName, "input", tool.Truncate(string(call.ToolInput), logInputLimit),
			"error", err != nil, "duration", time.Since(start).Round(time.Millisecond))
	}()
	return t.Run(ctx, call.ToolInput)
}

func (r *run) deny(ctx context.Context, call event.ToolCall, reason string) llm.ToolResult {
	if reason == "" {
		reason = "blocked by a hook"
	}
	r.fire(ctx, event.PermissionDenied{ToolCall: call, Reason: reason})
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
