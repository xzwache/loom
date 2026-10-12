package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/xzwache/loom/internal/agent"
	"github.com/xzwache/loom/internal/config"
	"github.com/xzwache/loom/internal/event"
	"github.com/xzwache/loom/internal/hook"
	"github.com/xzwache/loom/internal/hook/command"
	"github.com/xzwache/loom/internal/llm"
	"github.com/xzwache/loom/internal/permission"
	"github.com/xzwache/loom/internal/session"
	"github.com/xzwache/loom/internal/tool"
	"github.com/xzwache/loom/internal/tool/bash"
	"github.com/xzwache/loom/internal/tool/edit"
	"github.com/xzwache/loom/internal/tool/glob"
	"github.com/xzwache/loom/internal/tool/read"
	"github.com/xzwache/loom/internal/tool/write"
	"github.com/xzwache/loom/internal/trace"
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

	store := session.NewFileStore(filepath.Join(stateHome(e.getenv), "loom", "sessions"))
	sess, source, err := openSession(ctx, store, opts, ws.Dir(), cfg.Model)
	if err != nil {
		fmt.Fprintf(e.stderr, "loom: %v\n", err)
		return exitError
	}
	log.Info("session", "id", sess.ID, "source", source)

	runID := trace.NewRunID(time.Now())
	hooks := &hook.Pipeline{}
	if opts.trace {
		closeTrace, err := startTrace(hooks, runID, ws.Dir(), cfg, log)
		if err != nil {
			fmt.Fprintf(e.stderr, "loom: %v\n", err)
			return exitError
		}
		defer closeTrace()
	}

	err = command.Register(hooks, cfg.Hooks, command.Env{
		SessionID:      sess.ID,
		Dir:            ws.Dir(),
		PermissionMode: string(cfg.Permissions.Mode),
		Log:            log,
	})
	if err != nil {
		fmt.Fprintf(e.stderr, "loom: %v\n", err)
		return exitError
	}
	policy, err := permission.New(cfg.Permissions, ws.Dir(), e.getenv("HOME"))
	if err != nil {
		fmt.Fprintf(e.stderr, "loom: %v\n", err)
		return exitError
	}
	// Registered after every hook that can rewrite tool input, so it judges
	// the input that will actually run.
	hook.On(hooks, policy.Hook)

	system := systemPrompt(ws.Dir())
	start, err := hooks.Fire(ctx, event.SessionStart{Source: source, Model: cfg.Model, Cwd: ws.Dir()})
	if err != nil {
		log.Warn("hook failed", "event", "SessionStart", "error", err)
	}
	if start.AdditionalContext != "" {
		system += "\n\n" + start.AdditionalContext
	}

	a := &agent.Agent{
		Model:     e.newModel(cfg),
		ModelID:   cfg.Model,
		System:    system,
		MaxTokens: cfg.MaxTokens,
		Tools:     tools,
		Budget: agent.Budget{
			MaxTurns:    cfg.MaxTurns,
			MaxTokens:   cfg.MaxRunTokens,
			MaxDuration: cfg.MaxDuration.Std(),
		},
		Hooks: hooks,
		Log:   log,
	}

	sess.History.AddPrompt(opts.task)
	res, err := a.Run(ctx, sess.History.Messages())
	if err := saveSession(context.WithoutCancel(ctx), store, &sess, res); err != nil {
		log.Warn("session not saved", "error", err)
	}
	endReason := string(res.Stop)
	if err != nil {
		endReason = "error"
	}
	if _, err := hooks.Fire(context.WithoutCancel(ctx), event.SessionEnd{Reason: endReason}); err != nil {
		log.Warn("hook failed", "event", "SessionEnd", "error", err)
	}
	if err != nil {
		fmt.Fprintf(e.stderr, "loom: %v\n", err)
		return exitError
	}

	if opts.jsonOut {
		out := headlessOutput{SessionID: sess.ID, Stop: res.Stop, Text: res.Text, Turns: res.Turns, Usage: res.Usage}
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
	case agent.StopHalted:
		fmt.Fprintf(e.stderr, "loom: stopped by a hook: %s\n", res.Detail)
		return exitIncomplete
	default:
		fmt.Fprintf(e.stderr, "loom: stopped before finishing: %s\n", res.Stop)
		return exitIncomplete
	}
}

// openSession returns the session to run in and the SessionStart source:
// "resume" for an existing one, "startup" for a new one.
func openSession(ctx context.Context, store session.Store, opts headlessOptions, cwd, model string) (session.Session, string, error) {
	switch {
	case opts.resume != "":
		s, err := store.Load(ctx, opts.resume)
		return s, "resume", err
	case opts.latest:
		s, err := session.Latest(ctx, store)
		if errors.Is(err, session.ErrNotFound) {
			return s, "", errors.New("no session to continue")
		}
		return s, "resume", err
	default:
		return session.New(time.Now(), cwd, model), "startup", nil
	}
}

// saveSession stores the history Run returned, which is always valid to send
// again, even after an error or a budget stop.
func saveSession(ctx context.Context, store session.Store, sess *session.Session, res agent.Result) error {
	if len(res.Messages) == 0 {
		return nil
	}
	history, err := session.NewHistory(res.Messages)
	if err != nil {
		return err
	}
	sess.History, sess.Usage, sess.Updated = history, sess.Usage.Add(res.Usage), time.Now()
	return store.Save(ctx, *sess)
}

func stateHome(getenv func(string) string) string {
	if dir := getenv("XDG_STATE_HOME"); dir != "" {
		return dir
	}
	return filepath.Join(getenv("HOME"), ".local", "state")
}

// startTrace records every event to .loom/runs/<run-id>.jsonl in workdir.
func startTrace(hooks *hook.Pipeline, runID, workdir string, cfg config.Config, log *slog.Logger) (func(), error) {
	f, err := trace.Create(filepath.Join(workdir, ".loom", "runs"), runID)
	if err != nil {
		return nil, err
	}
	hooks.OnAll(trace.New(f, runID, cfg.APIKey.Reveal()))
	log.Info("trace", "path", f.Name())
	return func() {
		if err := f.Close(); err != nil {
			log.Warn("trace", "error", err)
		}
	}, nil
}

func systemPrompt(workdir string) string {
	return "You are loom, a coding agent. You work in " + workdir + " using the tools provided.\n" +
		"Inspect before you change things, verify your changes (for example by running the tests), " +
		"and finish with a short summary of what you did."
}
