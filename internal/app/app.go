// Package app puts loom together from its config: the workspace, tools, hooks,
// permission policy, skills, subagents, prompts and session. Headless mode and
// the interactive UI both build a Conversation, so they behave the same.
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
	// Observe sees every event from the start of Build on.
	Observe hook.Observer
	// Approve asks the user about tool calls the policy asks about; nil
	// denies them, as headless mode does.
	Approve agent.Approver
}

// Conversation is one session and the agent that works in it.
type Conversation struct {
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

// Build puts a Conversation together. Close releases it.
func Build(ctx context.Context, cfg config.Config, opts Options) (*Conversation, error) {
	return newBuilder(cfg, opts).build(ctx)
}

func (b *builder) build(ctx context.Context) (*Conversation, error) {
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
func (c *Conversation) Run(ctx context.Context, prompt string) (agent.Result, error) {
	notes, spent := c.memoryHint(ctx, prompt)
	c.Session.History.AddPrompt(prompt, notes...)
	res, err := c.agent.RunAfter(ctx, c.Session.History.Messages(), spent)
	c.lastStop = res.Stop
	// A compaction may have rebuilt the prompt; the next run goes on with it.
	if res.System != "" {
		c.agent.System = res.System
	}
	if err := saveSession(context.WithoutCancel(ctx), c.store, &c.Session, res); err != nil {
		c.log.Warn("session not saved", "error", err)
	}
	return res, err
}

// Compact compacts the history now and saves it. The bool reports whether
// the history changed.
func (c *Conversation) Compact(ctx context.Context) (agent.Result, bool, error) {
	res, changed, err := c.agent.Compact(ctx, c.Session.History.Messages())
	if err != nil || !changed {
		return res, changed, err
	}
	if res.System != "" {
		c.agent.System = res.System
	}
	if err := saveSession(context.WithoutCancel(ctx), c.store, &c.Session, res); err != nil {
		c.log.Warn("session not saved", "error", err)
	}
	return res, true, nil
}

// End saves memories from the session and cleans them up, as configured,
// then reports that the session is over. It returns the tokens that took;
// they are added to the session's usage.
func (c *Conversation) End(ctx context.Context, reason string) llm.Usage {
	used := c.endMemory(ctx)
	if used.Total() > 0 {
		c.Session.Usage, c.Session.Updated = c.Session.Usage.Add(used), time.Now()
		if err := c.store.Save(ctx, c.Session); err != nil {
			c.log.Warn("session not saved", "error", err)
		}
	}
	c.hooks.Notify(ctx, event.SessionEnd{Reason: reason})
	return used
}

// Close releases what Build opened, in reverse order.
func (c *Conversation) Close() {
	for i := len(c.closers) - 1; i >= 0; i-- {
		c.closers[i]()
	}
	c.closers = nil
}
