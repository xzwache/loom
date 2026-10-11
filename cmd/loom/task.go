package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/xzwache/loom/internal/agent"
	"github.com/xzwache/loom/internal/config"
	"github.com/xzwache/loom/internal/llm"
	"github.com/xzwache/loom/internal/tool"
	"github.com/xzwache/loom/internal/tool/bash"
	"github.com/xzwache/loom/internal/tool/edit"
	"github.com/xzwache/loom/internal/tool/glob"
	"github.com/xzwache/loom/internal/tool/read"
	"github.com/xzwache/loom/internal/tool/write"
)

type taskOutput struct {
	Stop  agent.StopReason `json:"stop"`
	Text  string           `json:"text"`
	Turns int              `json:"turns"`
	Usage llm.Usage        `json:"usage"`
}

func runTask(ctx context.Context, cfg config.Config, task string, jsonOut bool, log *slog.Logger, e env) int {
	ws, err := tool.OpenFileSystemWorkspace(e.workdir)
	if err != nil {
		fmt.Fprintf(e.stderr, "loom: %v\n", err)
		return exitError
	}
	defer ws.Close()
	tools, err := tool.NewRegistry(
		&bash.Tool{Dir: ws.Dir(), Timeout: cfg.BashTimeout.Std(), MaxOutput: cfg.MaxToolOutput},
		&read.Tool{Workspace: ws, MaxOutput: cfg.MaxToolOutput},
		&write.Tool{Workspace: ws},
		&edit.Tool{Workspace: ws},
		&glob.Tool{Workspace: ws},
	)
	if err != nil {
		fmt.Fprintf(e.stderr, "loom: %v\n", err)
		return exitError
	}

	a := &agent.Agent{
		Model:     e.newModel(cfg),
		ModelID:   cfg.Model,
		System:    systemPrompt(ws.Dir()),
		MaxTokens: cfg.MaxTokens,
		Tools:     tools,
		Budget: agent.Budget{
			MaxTurns:    cfg.MaxTurns,
			MaxTokens:   cfg.MaxRunTokens,
			MaxDuration: cfg.MaxDuration.Std(),
		},
		Log: log,
	}

	res, err := a.Run(ctx, []llm.Message{{Role: llm.RoleUser, Content: []llm.Block{llm.Text{Text: task}}}})
	if err != nil {
		fmt.Fprintf(e.stderr, "loom: %v\n", err)
		return exitError
	}

	if jsonOut {
		out := taskOutput{Stop: res.Stop, Text: res.Text, Turns: res.Turns, Usage: res.Usage}
		if err := json.NewEncoder(e.stdout).Encode(out); err != nil {
			fmt.Fprintf(e.stderr, "loom: %v\n", err)
			return exitError
		}
	} else if res.Text != "" {
		fmt.Fprintln(e.stdout, res.Text)
	}
	log.Info("done", "stop", res.Stop, "turns", res.Turns,
		"input_tokens", res.Usage.InputTokens, "output_tokens", res.Usage.OutputTokens,
		"cache_read_tokens", res.Usage.CacheReadTokens, "cache_write_tokens", res.Usage.CacheWriteTokens)

	switch res.Stop {
	case agent.StopDone:
		return exitOK
	case agent.StopCancelled:
		return exitInterrupted
	default:
		fmt.Fprintf(e.stderr, "loom: stopped before finishing: %s\n", res.Stop)
		return exitIncomplete
	}
}

func systemPrompt(workdir string) string {
	return "You are loom, a coding agent. You work in " + workdir + " using the tools provided.\n" +
		"Inspect before you change things, verify your changes (for example by running the tests), " +
		"and finish with a short summary of what you did."
}
