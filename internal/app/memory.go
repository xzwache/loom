package app

import (
	"cmp"
	"context"
	"sync/atomic"

	"github.com/xzwache/loom/internal/agent"
	"github.com/xzwache/loom/internal/config"
	"github.com/xzwache/loom/internal/event"
	"github.com/xzwache/loom/internal/llm"
	"github.com/xzwache/loom/internal/memory"
	"github.com/xzwache/loom/internal/prompt"
)

// Who changed a memory, in MemoryWrite.
const (
	byModel         = "model"
	byExtraction    = "extraction"
	byConsolidation = "consolidation"
)

// maxSelected caps the memories named with one request.
const maxSelected = 5

// appMemory is the Conversation's memory state; Conversation.memory is nil when memory is off.
type appMemory struct {
	cfg    config.Memory
	store  *memory.Store
	helper memory.Helper
	// unlisted are the memories that did not fit the main prompt when it was
	// last built; selection picks from them for each request.
	unlisted []memory.Memory
	// from is where this process's messages start in the history.
	from int
	// writes counts memory changes in this session.
	writes atomic.Int32
}

// addMemory registers the memory tool; subagents get a read-only one. The
// memory list goes into the prompts when they are built: the main one in
// newAgent and again after each compaction, a subagent's when it starts. So
// a list is never older than the cached prompt it is part of.
func (b *builder) addMemory(context.Context) error {
	if !b.cfg.Memory.Enabled {
		return nil
	}
	store := memory.NewStore(b.userPath("memory"), b.ws.Dir())
	mems, problems := store.List()
	for _, p := range problems {
		b.app.log.Warn("memory left out", "error", p)
	}
	b.app.log.Info("memory", "memories", len(mems), "project", store.Dir(memory.ScopeProject))
	m := &appMemory{
		cfg:   b.cfg.Memory,
		store: store,
		helper: memory.Helper{
			Model:     b.opts.Model,
			ModelID:   cmp.Or(b.cfg.Memory.Model, b.cfg.Subagent.Model, b.cfg.Model),
			MaxTokens: b.cfg.MaxTokens,
			MaxInput:  b.cfg.Compaction.ContextWindow * 2,
		},
		from: len(b.app.Session.History.Messages()),
	}
	b.app.memory = m

	hooks := b.app.hooks
	onRead := func(ctx context.Context, path string) { hooks.Notify(ctx, event.MemoryRead{Path: path}) }
	if err := b.tools.Register(&memory.Tool{
		Store:  store,
		OnRead: onRead,
		OnWrite: func(ctx context.Context, path, action string) {
			m.writes.Add(1)
			hooks.Notify(ctx, event.MemoryWrite{Path: path, Action: action, By: byModel})
		},
	}); err != nil {
		return err
	}
	b.subagentMemory = &memory.Tool{Store: store, ReadOnly: true, OnRead: onRead}
	return nil
}

// mainPrompt is base with the current memory list. Memories that do not fit
// become the candidates for selection.
func (m *appMemory) mainPrompt(base string) string {
	mems, _ := m.store.List()
	_, m.unlisted = memory.Fit(mems, memory.MaxCatalog)
	return prompt.Build(prompt.Section{Text: base}, memory.Section(mems, false))
}

func (m *appMemory) subagentPrompt() string {
	mems, _ := m.store.List()
	return prompt.Build(memory.Section(mems, true))
}

// memoryHint picks the unlisted memories that fit the request, as a note to
// send with it. A failed selection is logged and the request goes alone. The
// tokens it used belong to the run.
func (c *Conversation) memoryHint(ctx context.Context, request string) ([]llm.Text, llm.Usage) {
	if c.memory == nil || len(c.memory.unlisted) == 0 {
		return nil, llm.Usage{}
	}
	picked, usage, err := c.memory.helper.Select(ctx, request, c.memory.unlisted, maxSelected)
	if err != nil {
		c.log.Warn("memory selection failed", "error", err)
		return nil, usage
	}
	c.logUsage("memory selected", usage, "picked", len(picked))
	if len(picked) == 0 {
		return nil, usage
	}
	return []llm.Text{llm.SystemText(llm.KindMemoryHint, memory.Hint(picked))}, usage
}

// endMemory runs extraction and consolidation and returns the tokens they
// used. After a run stopped by its token budget it runs neither.
func (c *Conversation) endMemory(ctx context.Context) llm.Usage {
	if c.memory == nil {
		return llm.Usage{}
	}
	if c.lastStop == agent.StopMaxRunTokens {
		c.log.Info("memory extraction and consolidation skipped: the run used its token budget")
		return llm.Usage{}
	}
	return c.extractMemories(ctx).Add(c.consolidateMemories(ctx))
}

// extractMemories saves what is worth keeping from this session's new
// messages. Failures are logged; they never fail the session.
func (c *Conversation) extractMemories(ctx context.Context) llm.Usage {
	m := c.memory
	if !m.cfg.AutoExtract {
		return llm.Usage{}
	}
	msgs := c.Session.History.Messages()
	switch {
	case len(msgs) == m.from:
		return llm.Usage{}
	case len(msgs) > m.from:
		// Messages from before a resume were looked at when they were new.
		// Fewer messages than at the start means compaction ran: take all.
		msgs = msgs[m.from:]
	}
	known, _ := m.store.List()
	out, err := m.helper.Extract(ctx, msgs, known)
	if err != nil {
		c.log.Warn("memory extraction failed", "error", err)
		return out.Usage
	}
	for _, r := range out.Rejected {
		c.log.Warn("extracted memory rejected", "error", r)
	}
	saved := 0
	for _, mem := range out.Memories {
		created, err := m.store.Write(mem)
		if err != nil {
			c.log.Warn("extracted memory not saved", "name", mem.Name, "error", err)
			continue
		}
		saved++
		m.writes.Add(1)
		action := memory.ActionUpdate
		if created {
			action = memory.ActionCreate
		}
		c.hooks.Notify(ctx, event.MemoryWrite{Path: m.store.Path(mem.Scope, mem.Name), Action: action, By: byExtraction})
	}
	c.logUsage("memory extracted", out.Usage, "saved", saved, "rejected", len(out.Rejected))
	return out.Usage
}

// consolidateMemories cleans up the memories once there are consolidate_at
// of them, only after a session that changed one, so an unchanged memory is
// not sent again and again.
func (c *Conversation) consolidateMemories(ctx context.Context) llm.Usage {
	m := c.memory
	if m.cfg.ConsolidateAt == 0 || m.writes.Load() == 0 {
		return llm.Usage{}
	}
	mems, problems := m.store.List()
	if len(mems) < m.cfg.ConsolidateAt {
		return llm.Usage{}
	}
	if len(problems) > 0 {
		// Unreadable files would look deleted to the model.
		c.log.Warn("memory consolidation skipped: some memory files are not readable", "files", len(problems))
		return llm.Usage{}
	}
	plan, usage, err := m.helper.Consolidate(ctx, mems)
	if err != nil {
		c.log.Warn("memory consolidation failed", "error", err)
		return usage
	}
	changes, err := m.store.Apply(plan)
	if err != nil {
		c.log.Warn("memory consolidation not applied; files restored", "error", err)
		return usage
	}
	for _, change := range changes {
		c.hooks.Notify(ctx, event.MemoryWrite{Path: change.Path, Action: change.Action, By: byConsolidation})
	}
	c.logUsage("memory consolidated", usage, "before", len(mems), "written", len(plan.Write), "deleted", len(plan.Delete))
	return usage
}

func (c *Conversation) logUsage(msg string, u llm.Usage, args ...any) {
	c.log.Info(msg, append(args, "input_tokens", u.InputTokens, "output_tokens", u.OutputTokens)...)
}
