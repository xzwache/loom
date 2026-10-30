package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"

	"github.com/xzwache/loom/internal/agent"
	"github.com/xzwache/loom/internal/app"
	"github.com/xzwache/loom/internal/config"
	"github.com/xzwache/loom/internal/llm"
)

type headlessOutput struct {
	SessionID string           `json:"session_id"`
	Stop      agent.StopReason `json:"stop"`
	Text      string           `json:"text"`
	Turns     int              `json:"turns"`
	Usage     llm.Usage        `json:"usage"`
}

type headlessOptions struct {
	task    string
	jsonOut bool
	trace   bool
	// resume is a session id; latest continues the most recent session.
	resume string
	latest bool
}

func runHeadless(ctx context.Context, cfg config.Config, opts headlessOptions, log *slog.Logger, e env) int {
	a, err := app.Build(ctx, cfg, app.Options{
		Version: version,
		Workdir: e.workdir,
		Dirs:    app.UserDirs(e.getenv),
		Model:   e.newModel(cfg),
		Log:     log,
		Trace:   opts.trace,
		Resume:  opts.resume,
		Latest:  opts.latest,
	})
	if err != nil {
		fmt.Fprintf(e.stderr, "loom: %v\n", err)
		return exitError
	}
	defer a.Close()

	res, err := a.Run(ctx, opts.task)
	// Memory calls at the end count in this invocation's usage.
	res.Usage = res.Usage.Add(a.End(context.WithoutCancel(ctx), endReason(res, err)))
	if err != nil {
		fmt.Fprintf(e.stderr, "loom: %v\n", err)
		return exitError
	}
	if err := printResult(e.stdout, a.Session.ID, res, opts.jsonOut); err != nil {
		fmt.Fprintf(e.stderr, "loom: %v\n", err)
		return exitError
	}
	log.Info("done", "stop", res.Stop, "turns", res.Turns,
		"input_tokens", res.Usage.InputTokens, "output_tokens", res.Usage.OutputTokens,
		"cache_read_tokens", res.Usage.CacheReadTokens, "cache_write_tokens", res.Usage.CacheWriteTokens)
	return exitCode(res, e.stderr)
}

func endReason(res agent.Result, err error) string {
	if err != nil {
		return "error"
	}
	return string(res.Stop)
}

func printResult(w io.Writer, sessionID string, res agent.Result, jsonOut bool) error {
	if jsonOut {
		out := headlessOutput{SessionID: sessionID, Stop: res.Stop, Text: res.Text, Turns: res.Turns, Usage: res.Usage}
		return json.NewEncoder(w).Encode(out)
	}
	if res.Text != "" {
		_, err := fmt.Fprintln(w, res.Text)
		return err
	}
	return nil
}

func exitCode(res agent.Result, stderr io.Writer) int {
	switch res.Stop {
	case agent.StopDone:
		return exitOK
	case agent.StopCancelled:
		return exitInterrupted
	case agent.StopHalted:
		fmt.Fprintf(stderr, "loom: stopped by a hook: %s\n", res.Detail)
		return exitIncomplete
	default:
		fmt.Fprintf(stderr, "loom: stopped before finishing: %s\n", res.Stop)
		return exitIncomplete
	}
}
