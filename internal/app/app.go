// Package app puts loom together from its config: the workspace, tools, hooks,
// permission policy, skills, subagents, prompts and session. Headless mode and
// the interactive UI both build an App, so they behave the same.
//
// Build runs named steps in a fixed order (see Build); each step adds one part
// and may use the parts before it.
package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/xzwache/loom/internal/agent"
	"github.com/xzwache/loom/internal/config"
	"github.com/xzwache/loom/internal/event"
	"github.com/xzwache/loom/internal/hook"
	"github.com/xzwache/loom/internal/llm"
	"github.com/xzwache/loom/internal/session"
)

type Options struct {
	Version string // loom's version, sent to MCP servers
	Workdir string
	Dirs    Dirs
	Model   llm.Model
	Log     *slog.Logger
	// Trace records every event to .loom/runs/<run-id>.jsonl.
	Trace bool
	// Resume is a session id to continue; Latest continues the most recent one.
	Resume string
	Latest bool
}

// App is a ready-to-run loom: one session and the agent that works in it.
type App struct {
	Session session.Session
	agent   *agent.Agent
	hooks   *hook.Pipeline
	store   session.Store
	log     *slog.Logger
	closers []func()
	memory  *appMemory
	// lastStop is how the last run ended.
	lastStop agent.StopReason
}

// Build puts an App together. Close releases it.
func Build(ctx context.Context, cfg config.Config, opts Options) (*App, error) {
	return newBuilder(cfg, opts).build(ctx)
}

func (b *builder) build(ctx context.Context) (*App, error) {
	steps := []func(context.Context) error{
		b.openWorkspace,
		b.addTools,
		b.openSession,
		b.startTrace,
		b.addHooks,
		b.addSkills,
		b.loadPrompts,
		b.addMemory,
		b.addTasks,
		b.startSession,
		b.addMCP,
		b.newCompaction,
		b.addSubagents,
		b.newAgent,
	}

	for _, step := range steps {
		if err := step(ctx); err != nil {
			b.app.Close()
			return nil, err
		}
	}

	return b.app, nil
}

// Run gives the agent one prompt in the session and saves the session after,
// even when the run fails or stops early.
func (a *App) Run(ctx context.Context, prompt string) (agent.Result, error) {
	notes, spent := a.memoryHint(ctx, prompt)
	a.Session.History.AddPrompt(prompt, notes...)
	res, err := a.agent.RunAfter(ctx, a.Session.History.Messages(), spent)
	a.lastStop = res.Stop
	// A compaction may have rebuilt the prompt; the next run goes on with it.
	if res.System != "" {
		a.agent.System = res.System
	}
	if err := saveSession(context.WithoutCancel(ctx), a.store, &a.Session, res); err != nil {
		a.log.Warn("session not saved", "error", err)
	}
	return res, err
}

// End saves memories from the session and cleans them up, as configured,
// then reports that the session is over. It returns the tokens that took;
// they are added to the session's usage.
func (a *App) End(ctx context.Context, reason string) llm.Usage {
	used := a.endMemory(ctx)
	if used.Total() > 0 {
		a.Session.Usage, a.Session.Updated = a.Session.Usage.Add(used), time.Now()
		if err := a.store.Save(ctx, a.Session); err != nil {
			a.log.Warn("session not saved", "error", err)
		}
	}
	a.hooks.Notify(ctx, event.SessionEnd{Reason: reason})
	return used
}

// Close releases what Build opened, in reverse order.
func (a *App) Close() {
	for i := len(a.closers) - 1; i >= 0; i-- {
		a.closers[i]()
	}
	a.closers = nil
}
