package frontend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/xzwache/loom/internal/agent"
	"github.com/xzwache/loom/internal/app"
	"github.com/xzwache/loom/internal/config"
	"github.com/xzwache/loom/internal/event"
	"github.com/xzwache/loom/internal/llm"
	"github.com/xzwache/loom/internal/llm/llmtest"
)

// recorder is a UI that keeps every update; onUpdate, if set, runs on each.
type recorder struct {
	updates  []Update
	onUpdate func(Update)
}

func (r *recorder) Update(u Update) {
	r.updates = append(r.updates, u)
	if r.onUpdate != nil {
		r.onUpdate(u)
	}
}

// kinds names the updates in order, with the tool status for ToolFinished.
func (r *recorder) kinds() []string {
	var out []string
	for _, u := range r.updates {
		k := fmt.Sprintf("%T", u.Body)[len("frontend."):]
		if f, ok := u.Body.(ToolFinished); ok {
			k += ":" + string(f.Status)
		}
		out = append(out, k)
	}
	return out
}

func open(t *testing.T, ui UI, replies ...llmtest.Reply) *Session {
	t.Helper()
	cfg := config.Default()
	cfg.Model = "test-model"
	cfg.Memory = config.Memory{} // ending a session would call the model to extract memories
	opts := app.Options{
		Workdir: t.TempDir(),
		Dirs:    app.Dirs{Home: t.TempDir(), User: t.TempDir(), State: t.TempDir()},
		Model:   llmtest.NewReplay(replies...),
	}
	s, err := Open(context.Background(), cfg, opts, ui)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(context.Background(), EndOther) })
	return s
}

func reply(stop llm.StopReason, content ...llm.Block) llmtest.Reply {
	return llmtest.Reply{Response: llm.Response{Content: content, StopReason: stop, Usage: llm.Usage{InputTokens: 10, OutputTokens: 2}}}
}

func todoUse(id string) llm.ToolUse {
	return llm.ToolUse{ID: id, Name: "todo", Input: json.RawMessage(`{"todos":[{"content":"fix it","status":"in_progress"}]}`)}
}

func TestPromptSendsUpdatesInOrder(t *testing.T) {
	ui := &recorder{}
	s := open(t, ui,
		reply(llm.StopToolUse, todoUse("t1")),
		reply(llm.StopEndTurn, llm.Text{Text: "all done"}),
	)

	res, err := s.Prompt(context.Background(), "fix the bug")
	if err != nil || res.Stop != agent.StopDone {
		t.Fatalf("Prompt = %v, %v", res.Stop, err)
	}
	want := []string{
		"SessionStarted", "ToolPending", "Usage", "ToolStarted", "PlanChanged", "ToolFinished:done",
		"TextDelta", "TextDelta", "Usage", "TurnDone",
	}
	if diff := cmp.Diff(want, ui.kinds()); diff != "" {
		t.Errorf("updates (-want +got):\n%s", diff)
	}
	plan := []event.TodoItem{{Content: "fix it", Status: "in_progress"}}
	want2 := State{Plan: plan, Usage: llm.Usage{InputTokens: 20, OutputTokens: 4}}
	if diff := cmp.Diff(want2, s.State(), cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("state after the prompt (-want +got):\n%s", diff)
	}
	done := ui.updates[len(ui.updates)-1].Body.(TurnDone)
	if done.Stop != agent.StopDone || done.Usage.Total() != 24 {
		t.Errorf("TurnDone = %+v", done)
	}
}

func TestCancelFinishesOpenToolsAndKeepsTheSession(t *testing.T) {
	ui := &recorder{}
	s := open(t, ui,
		reply(llm.StopToolUse, llm.Text{Text: "on it"}, todoUse("t1")),
		reply(llm.StopEndTurn, llm.Text{Text: "again"}),
	)
	ui.onUpdate = func(u Update) {
		if _, ok := u.Body.(TextDelta); ok {
			s.Cancel()
		}
	}

	res, err := s.Prompt(context.Background(), "fix the bug")
	if err != nil || res.Stop != agent.StopCancelled {
		t.Fatalf("Prompt = %v, %v; want cancelled", res.Stop, err)
	}
	want := []string{"SessionStarted", "TextDelta", "TextDelta", "ToolPending", "Usage", "ToolFinished:cancelled", "TurnDone"}
	if diff := cmp.Diff(want, ui.kinds()); diff != "" {
		t.Errorf("updates (-want +got):\n%s", diff)
	}
	if open := s.State().Tools; len(open) != 0 {
		t.Errorf("tools still open after the turn: %+v", open)
	}

	ui.onUpdate, ui.updates = nil, nil
	if res, err := s.Prompt(context.Background(), "go on"); err != nil || res.Stop != agent.StopDone || res.Text != "again" {
		t.Errorf("prompt after cancel = %+v, %v", res, err)
	}
}

func TestPromptWhileOneRunsIsBusy(t *testing.T) {
	ui := &recorder{}
	s := open(t, ui, reply(llm.StopEndTurn, llm.Text{Text: "hi"}))
	var second error
	ui.onUpdate = func(u Update) {
		if _, ok := u.Body.(TextDelta); ok && second == nil {
			_, second = s.Prompt(context.Background(), "me too")
		}
	}

	if _, err := s.Prompt(context.Background(), "first"); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(second, ErrBusy) {
		t.Errorf("second prompt error = %v, want ErrBusy", second)
	}
}

func TestStateChecksUpdateOrder(t *testing.T) {
	var s state
	sub := event.Agent{ID: "a1", Type: "general"}
	steps := []struct {
		body    Body
		wantErr bool
	}{
		{ToolStarted{ID: "t1"}, true},
		{ToolPending{ID: "t1", Name: "read"}, false},
		{ToolPending{ID: "t1", Name: "read"}, true},
		{ToolStarted{ID: "t1", Input: json.RawMessage(`{}`)}, false},
		{ToolStarted{ID: "t1"}, true},
		{ToolFinished{ID: "t1", Status: StatusDone}, false},
		{ToolFinished{ID: "t1", Status: StatusDone}, true},
		{SubagentStarted{ID: "a1", Type: "general"}, false},
		{SubagentStarted{ID: "a1", Type: "general"}, true},
		{SubagentStopped{ID: "a1"}, false},
		{SubagentStopped{ID: "a1"}, true},
	}
	for i, step := range steps {
		if err := step.body.apply(&s, sub); (err != nil) != step.wantErr {
			t.Errorf("step %d %T: error %v, want error %v", i, step.body, err, step.wantErr)
		}
	}
	if got := s.snapshot(); len(got.Tools) != 0 || len(got.Subagents) != 0 {
		t.Errorf("state left open: %+v", got)
	}
}
