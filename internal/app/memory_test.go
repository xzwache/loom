package app

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xzwache/loom/internal/agent"
	"github.com/xzwache/loom/internal/config"
	"github.com/xzwache/loom/internal/event"
	"github.com/xzwache/loom/internal/hook"
	"github.com/xzwache/loom/internal/llm"
	"github.com/xzwache/loom/internal/llm/llmtest"
	"github.com/xzwache/loom/internal/memory"
	"github.com/xzwache/loom/internal/tool"
)

func memoryApp(t *testing.T, cfg config.Config, model *llmtest.Scripted, mems ...memory.Memory) (*App, *builder) {
	t.Helper()
	cfg.Model = "test-model"
	workdir, user := t.TempDir(), t.TempDir()
	store := memory.NewStore(filepath.Join(user, "memory"), workdir)
	for _, m := range mems {
		if _, err := store.Write(m); err != nil {
			t.Fatal(err)
		}
	}
	b := newBuilder(cfg, Options{Workdir: workdir, Dirs: Dirs{User: user, State: t.TempDir()}, Model: model})
	a, err := b.build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return a, b
}

func TestMemoryOff(t *testing.T) {
	cfg := config.Default()
	cfg.Memory.Enabled = false
	a, b := memoryApp(t, cfg, llmtest.NewScripted())
	if _, ok := b.tools.Get(tool.Memory); ok || strings.Contains(a.agent.System, "# Memory") || b.subagentMemory != nil {
		t.Error("memory is set up although it is off")
	}
	a.End(context.Background(), "done")
}

func TestMemoryExtractedAtEnd(t *testing.T) {
	answer := `{"memories": [{"scope": "project", "name": "uses-make", "type": "project", "description": "build with make", "content": "Use make."}]}`
	model := llmtest.NewScripted(llmtest.TextReply("ok"), llmtest.TextReply(answer))
	a, b := memoryApp(t, config.Default(), model)
	writes := recordWrites(a)
	if sub, ok := b.subagentMemory.(*memory.Tool); !ok || !sub.ReadOnly {
		t.Error("subagents do not get a read-only memory tool")
	}

	a.End(context.Background(), "done")
	if len(model.Requests()) != 0 {
		t.Fatal("extraction ran without new messages")
	}
	if _, err := a.Run(context.Background(), "we build with make"); err != nil {
		t.Fatal(err)
	}
	a.End(context.Background(), "done")
	if w := *writes; len(w) != 1 || w[0].Action != "create" || w[0].By != byExtraction || !strings.HasSuffix(w[0].Path, "uses-make.md") {
		t.Errorf("MemoryWrite events = %+v", w)
	}
}

func fact(name string) memory.Memory {
	return memory.Memory{Scope: memory.ScopeGlobal, Name: name, Type: memory.TypeProject, Description: "fact " + name, Content: "text of " + name}
}

func recordWrites(a *App) *[]event.MemoryWrite {
	var writes []event.MemoryWrite
	hook.On(a.hooks, func(_ context.Context, e event.MemoryWrite) (hook.Outcome, error) {
		writes = append(writes, e)
		return hook.Outcome{}, nil
	})
	return &writes
}

func TestMemorySelectionForUnlisted(t *testing.T) {
	var mems []memory.Memory
	for i := range 400 {
		mems = append(mems, fact(fmt.Sprintf("m%03d", i)))
	}
	model := llmtest.NewScripted(llmtest.TextReply(`{"pick": [0]}`), llmtest.TextReply("ok"))
	cfg := config.Default()
	cfg.Memory.AutoExtract = false
	a, _ := memoryApp(t, cfg, model, mems...)
	_, unlisted := memory.Fit(mems, memory.MaxCatalog)
	if len(a.memory.unlisted) == 0 || a.memory.unlisted[0].Name != unlisted[0].Name {
		t.Fatalf("unlisted = %d memories", len(a.memory.unlisted))
	}

	if _, err := a.Run(context.Background(), "do the thing"); err != nil {
		t.Fatal(err)
	}
	reqs := model.Requests()
	if !strings.Contains(reqs[0].Messages[0].Content[0].(llm.Text).Text, "Request:\ndo the thing") {
		t.Error("the first request is not the selection")
	}
	content := reqs[1].Messages[0].Content
	typed, hint := content[0].(llm.Text), content[1].(llm.Text)
	if typed != llm.UserText("do the thing") || hint.Origin != llm.OriginSystem || hint.Kind != llm.KindMemoryHint ||
		!strings.HasPrefix(hint.Text, "Memories that may help") || !strings.Contains(hint.Text, "global/"+unlisted[0].Name) {
		t.Errorf("prompt = %+v; want the request, then the hint from loom", content)
	}
	if saved := a.Session.History.Messages()[0].Content; len(saved) != 2 || saved[1].(llm.Text).Kind != llm.KindMemoryHint {
		t.Errorf("the session keeps %+v; want the hint marked as from loom", saved)
	}
	if !strings.Contains(a.agent.System, "more memories are not listed here") {
		t.Error("the system prompt does not say some memories are unlisted")
	}
}

func TestMemoryConsolidation(t *testing.T) {
	write := `{"action":"write","scope":"global","name":"d","type":"user","description":"d","content":"d"}`
	plan := `{"write": [], "delete": [{"scope": "global", "name": "b"}]}`
	model := llmtest.NewScripted(
		llmtest.ToolUseReply(llm.ToolUse{ID: "t1", Name: "memory", Input: json.RawMessage(write)}),
		llmtest.TextReply("ok"),
		llmtest.TextReply(plan),
	)
	cfg := config.Default()
	cfg.Memory.AutoExtract = false
	cfg.Memory.ConsolidateAt = 4
	a, _ := memoryApp(t, cfg, model, fact("a"), fact("b"), fact("c"))
	writes := recordWrites(a)

	a.End(context.Background(), "done")
	if len(model.Requests()) != 0 {
		t.Fatal("consolidation ran in a session that changed no memory")
	}
	if _, err := a.Run(context.Background(), "remember d"); err != nil {
		t.Fatal(err)
	}
	a.End(context.Background(), "done")
	if len(model.Requests()) != 3 {
		t.Fatalf("got %d requests, want 2 turns and the consolidation", len(model.Requests()))
	}
	got := *writes
	if len(got) != 2 || got[1].By != byConsolidation || got[1].Action != memory.ActionDelete || !strings.HasSuffix(got[1].Path, "b.md") {
		t.Errorf("MemoryWrite events = %+v", got)
	}
	if mems, _ := a.memory.store.List(); len(mems) != 3 {
		t.Errorf("%d memories left, want a, c, d", len(mems))
	}
}

func TestSubagentSeesMemoryWrittenThisSession(t *testing.T) {
	write := `{"action":"write","scope":"project","name":"new-fact","type":"project","description":"written mid-session","content":"x"}`
	spawn := `{"description":"look","prompt":"look around"}`
	model := llmtest.NewScripted(
		llmtest.ToolUseReply(llm.ToolUse{ID: "t1", Name: "memory", Input: json.RawMessage(write)}),
		llmtest.ToolUseReply(llm.ToolUse{ID: "t2", Name: "agent", Input: json.RawMessage(spawn)}),
		llmtest.TextReply("report"),
		llmtest.TextReply("done"),
	)
	cfg := config.Default()
	cfg.Memory.AutoExtract = false
	a, _ := memoryApp(t, cfg, model)
	if strings.Contains(a.agent.System, "new-fact") {
		t.Fatal("the memory exists before the run")
	}
	if _, err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	sub := model.Requests()[2]
	if !strings.Contains(sub.System, "- new-fact (project): written mid-session") || !strings.Contains(sub.System, "read-only") {
		t.Errorf("subagent system prompt lacks the new memory:\n%s", sub.System)
	}
}

func TestEndCountsMemoryTokens(t *testing.T) {
	answer := llmtest.TextReply(`{"memories": []}`)
	answer.Response.Usage = llm.Usage{InputTokens: 70, OutputTokens: 5}
	model := llmtest.NewScripted(llmtest.TextReply("ok"), answer)
	a, _ := memoryApp(t, config.Default(), model)
	res, err := a.Run(context.Background(), "hi")
	if err != nil {
		t.Fatal(err)
	}
	before := a.Session.Usage
	if used := a.End(context.Background(), "done"); used.Total() != 75 {
		t.Errorf("End used %d tokens, want 75", used.Total())
	}
	if got := a.Session.Usage.Total() - before.Total(); got != 75 {
		t.Errorf("session usage grew by %d, want 75 (run used %d)", got, res.Usage.Total())
	}
	saved, err := a.store.Load(context.Background(), a.Session.ID)
	if err != nil || saved.Usage != a.Session.Usage {
		t.Errorf("saved usage = %+v, %v; want %+v", saved.Usage, err, a.Session.Usage)
	}
}

func TestEndSkipsMemoryAfterTokenBudgetStop(t *testing.T) {
	busy := llmtest.ToolUseReply(llm.ToolUse{ID: "t1", Name: "memory", Input: json.RawMessage(`{"action":"list"}`)})
	busy.Response.Usage = llm.Usage{InputTokens: 100}
	model := llmtest.NewScripted(busy)
	cfg := config.Default()
	cfg.MaxRunTokens = 50
	a, _ := memoryApp(t, cfg, model)
	res, err := a.Run(context.Background(), "hi")
	if err != nil || res.Stop != agent.StopMaxRunTokens {
		t.Fatalf("Run = %s, %v; want max_run_tokens", res.Stop, err)
	}
	if used := a.End(context.Background(), "max_run_tokens"); used.Total() != 0 || len(model.Requests()) != 1 {
		t.Errorf("End made %d more calls, %d tokens; want none", len(model.Requests())-1, used.Total())
	}
}
