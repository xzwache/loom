package agent

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/xzwache/loom/internal/llm"
	"github.com/xzwache/loom/internal/tool"
)

const logInputLimit = 200

func indexTools(tools []tool.Tool) (map[string]tool.Tool, []llm.ToolSpec, error) {
	byName := make(map[string]tool.Tool, len(tools))
	specs := make([]llm.ToolSpec, 0, len(tools))
	for _, t := range tools {
		spec := t.Spec()
		if _, dup := byName[spec.Name]; dup {
			return nil, nil, fmt.Errorf("duplicate tool name %q", spec.Name)
		}
		byName[spec.Name] = t
		specs = append(specs, spec)
	}
	return byName, specs, nil
}

// runTools answers every tool use in content, in order, in one user message.
func runTools(ctx context.Context, log *slog.Logger, tools map[string]tool.Tool, content []llm.Block) llm.Message {
	var results []llm.Block
	for _, b := range content {
		if use, ok := b.(llm.ToolUse); ok {
			results = append(results, runTool(ctx, log, tools, use))
		}
	}
	return llm.Message{Role: llm.RoleUser, Content: results}
}

func runTool(ctx context.Context, log *slog.Logger, tools map[string]tool.Tool, use llm.ToolUse) (res llm.ToolResult) {
	if ctx.Err() != nil {
		return failed(use.ID, "not run: the run was stopped")
	}
	t, ok := tools[use.Name]
	if !ok {
		return failed(use.ID, fmt.Sprintf("unknown tool %q", use.Name))
	}

	start := time.Now()
	defer func() {
		if r := recover(); r != nil {
			res = failed(use.ID, fmt.Sprintf("tool %s panicked: %v", use.Name, r))
		}
		log.Info("tool", "name", use.Name, "input", tool.Truncate(string(use.Input), logInputLimit),
			"error", res.IsError, "duration", time.Since(start).Round(time.Millisecond))
	}()

	out, err := t.Run(ctx, use.Input)
	if err != nil {
		return failed(use.ID, err.Error())
	}
	return llm.ToolResult{ToolUseID: use.ID, Content: out}
}

// skipTools answers tool uses that will not run, so the history stays valid.
// It returns nil when content has no tool uses.
func skipTools(content []llm.Block, reason string) *llm.Message {
	var results []llm.Block
	for _, b := range content {
		if use, ok := b.(llm.ToolUse); ok {
			results = append(results, failed(use.ID, reason))
		}
	}
	if results == nil {
		return nil
	}
	return &llm.Message{Role: llm.RoleUser, Content: results}
}

func failed(id, msg string) llm.ToolResult {
	return llm.ToolResult{ToolUseID: id, Content: msg, IsError: true}
}
