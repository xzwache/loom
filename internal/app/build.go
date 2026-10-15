package app

import (
	"cmp"
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/xzwache/loom/internal/agent"
	"github.com/xzwache/loom/internal/config"
	"github.com/xzwache/loom/internal/event"
	"github.com/xzwache/loom/internal/hook"
	"github.com/xzwache/loom/internal/hook/command"
	"github.com/xzwache/loom/internal/permission"
	"github.com/xzwache/loom/internal/prompt"
	"github.com/xzwache/loom/internal/session"
	"github.com/xzwache/loom/internal/skill"
	"github.com/xzwache/loom/internal/subagent"
	"github.com/xzwache/loom/internal/todo"
	"github.com/xzwache/loom/internal/tool"
	"github.com/xzwache/loom/internal/tool/bash"
	"github.com/xzwache/loom/internal/tool/edit"
	"github.com/xzwache/loom/internal/tool/glob"
	"github.com/xzwache/loom/internal/tool/read"
	"github.com/xzwache/loom/internal/tool/write"
	"github.com/xzwache/loom/internal/trace"
)

// builder holds the parts the build steps share while an App is put together.
type builder struct {
	cfg   config.Config
	opts  Options
	app   *App
	ws    *tool.FileSystemWorkspace
	todos *todo.List
	tools *tool.Registry
	// source is how the session started, for SessionStart.
	source string
	// system is the main agent's prompt; subagentContext is what every
	// subagent gets after its type's prompt.
	system          string
	subagentContext string
}

func newBuilder(cfg config.Config, opts Options) *builder {
	return &builder{
		cfg:  cfg,
		opts: opts,
		app:  &App{hooks: &hook.Pipeline{Log: opts.Log}, log: opts.Log},
	}
}

func (b *builder) userPath(parts ...string) string {
	return filepath.Join(append([]string{b.opts.Dirs.User}, parts...)...)
}

func (b *builder) projectPath(parts ...string) string {
	return filepath.Join(append([]string{b.ws.Dir(), ProjectDir}, parts...)...)
}

func (b *builder) openWorkspace(context.Context) error {
	ws, err := tool.OpenFileSystemWorkspace(b.opts.Workdir)
	if err != nil {
		return err
	}
	b.ws = ws
	b.app.closers = append(b.app.closers, func() { _ = ws.Close() })
	return nil
}

// addTools registers the built-in tools. The skill and agent tools come in
// their own steps.
func (b *builder) addTools(context.Context) error {
	hooks := b.app.hooks
	b.todos = todo.NewList(func(ctx context.Context, items []todo.Item) {
		hooks.Notify(ctx, todo.Updated(items))
	})
	tools, err := tool.NewRegistry(
		&bash.Tool{Dir: b.ws.Dir(), Timeout: b.cfg.BashTimeout.Std(), MaxOutput: b.cfg.MaxToolOutput},
		&read.Tool{Workspace: b.ws, MaxOutput: b.cfg.MaxToolOutput},
		&write.Tool{Workspace: b.ws},
		&edit.Tool{Workspace: b.ws},
		&glob.Tool{Workspace: b.ws},
		&todo.Tool{List: b.todos},
	)
	b.tools = tools
	return err
}

func (b *builder) openSession(ctx context.Context) error {
	b.app.store = session.NewFileStore(filepath.Join(b.opts.Dirs.State, "sessions"))
	sess, source, err := findSession(ctx, b.app.store, b.opts, b.ws.Dir(), b.cfg.Model)
	if err != nil {
		return err
	}
	b.app.Session, b.source = sess, source
	b.app.log.Info("session", "id", sess.ID, "source", source)
	return nil
}

// startTrace records every event to .loom/runs/<run-id>.jsonl when tracing
// is on. It comes before the other hooks, so it sees everything they do.
func (b *builder) startTrace(context.Context) error {
	if !b.opts.Trace {
		return nil
	}
	runID := trace.NewRunID(time.Now())
	f, err := trace.Create(b.projectPath("runs"), runID)
	if err != nil {
		return err
	}
	b.app.hooks.OnAll(trace.New(f, runID, b.cfg.APIKey.Reveal()))
	b.app.log.Info("trace", "path", f.Name())
	b.app.closers = append(b.app.closers, func() {
		if err := f.Close(); err != nil {
			b.app.log.Warn("trace", "error", err)
		}
	})
	return nil
}

// addHooks registers the command hooks, then the permission policy, after
// every hook that can rewrite tool input, so it judges the input that will
// actually run; then the todo reminder.
func (b *builder) addHooks(context.Context) error {
	hooks := b.app.hooks
	err := command.Register(hooks, b.cfg.Hooks, command.Env{
		SessionID:      b.app.Session.ID,
		Dir:            b.ws.Dir(),
		PermissionMode: string(b.cfg.Permissions.Mode),
		Log:            b.app.log,
	})
	if err != nil {
		return err
	}
	policy, err := permission.New(b.cfg.Permissions, b.ws.Dir(), b.opts.Dirs.Home)
	if err != nil {
		return err
	}
	hook.On(hooks, policy.Hook)
	hook.On(hooks, (&todo.Reminder{List: b.todos, After: b.cfg.TodoReminderTurns}).Hook)
	return nil
}

// addSkills registers the skill tool when any skill is found. It comes after
// the hooks, so they see SkillLoaded.
func (b *builder) addSkills(ctx context.Context) error {
	skills, err := skill.Load(b.userPath("skills"), b.projectPath("skills"))
	if err != nil || len(skills) == 0 {
		return err
	}
	hooks := b.app.hooks
	for _, s := range skills {
		hooks.Notify(ctx, event.SkillLoaded{Skill: s.Name, Path: filepath.Join(s.Dir, skill.FileName)})
	}
	return b.tools.Register(skill.NewTool(skills, func(ctx context.Context, s skill.Skill) {
		hooks.Notify(ctx, event.SkillUsed{Skill: s.Name})
	}))
}

// loadPrompts builds the system prompts. Fixed sections go first and the
// project ones after, so the cached prefix is shared across projects.
func (b *builder) loadPrompts(ctx context.Context) error {
	loader := prompt.Loader{Names: b.cfg.InstructionFiles, UserDir: b.opts.Dirs.User}
	found, err := loader.Load(b.ws.Dir())
	if err != nil {
		return err
	}
	for _, in := range found {
		b.app.hooks.Notify(ctx, event.InstructionsLoaded{Path: in.Path, Source: in.Source})
	}
	env, project := prompt.Environment(b.ws.Dir()), prompt.Instructions(found)
	b.system = prompt.Build(prompt.System(), env, project)
	b.subagentContext = prompt.Build(env, project)
	return nil
}

// startSession fires SessionStart; context its hooks return goes into the
// main prompt.
func (b *builder) startSession(ctx context.Context) error {
	out, err := b.app.hooks.Fire(ctx, event.SessionStart{Source: b.source, Model: b.cfg.Model, Cwd: b.ws.Dir()})
	if err != nil {
		b.app.log.Warn("hook failed", "event", "SessionStart", "error", err)
	}
	if out.AdditionalContext != "" {
		b.system += "\n\n" + out.AdditionalContext
	}
	return nil
}

// addSubagents registers the agent tool. Subagent tools are picked before it
// is registered, so subagents cannot start their own.
func (b *builder) addSubagents(context.Context) error {
	subTools, err := b.tools.Only(b.cfg.Subagent.Tools...)
	if err != nil {
		return fmt.Errorf("subagent tools: %w", err)
	}
	types, err := subagent.LoadTypes(b.userPath("agents"), b.projectPath("agents"))
	if err != nil {
		return err
	}
	sub := b.cfg.Subagent
	agentTool, err := subagent.New(agent.Agent{
		Model:     b.opts.Model,
		ModelID:   cmp.Or(sub.Model, b.cfg.Model),
		System:    b.subagentContext,
		Tools:     subTools,
		MaxTokens: b.cfg.MaxTokens,
		Budget:    agent.Budget{MaxTurns: sub.MaxTurns, MaxTokens: sub.MaxRunTokens, MaxDuration: b.cfg.MaxDuration.Std()},
		Hooks:     b.app.hooks,
		Log:       b.app.log,
	}, types)
	if err != nil {
		return err
	}
	return b.tools.Register(agentTool)
}

func (b *builder) newAgent(context.Context) error {
	b.app.agent = &agent.Agent{
		Model:     b.opts.Model,
		ModelID:   b.cfg.Model,
		System:    b.system,
		Tools:     b.tools,
		MaxTokens: b.cfg.MaxTokens,
		Budget:    agent.Budget{MaxTurns: b.cfg.MaxTurns, MaxTokens: b.cfg.MaxRunTokens, MaxDuration: b.cfg.MaxDuration.Std()},
		Hooks:     b.app.hooks,
		Log:       b.app.log,
	}
	return nil
}
