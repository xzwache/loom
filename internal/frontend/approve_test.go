package frontend

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/xzwache/loom/internal/agent"
	"github.com/xzwache/loom/internal/event"
	"github.com/xzwache/loom/internal/llm"
)

// approvingUI records updates and answers approvals with answer.
type approvingUI struct {
	recorder
	answer agent.Answer
	ask    func(ctx context.Context, a agent.Approval)
}

func (u *approvingUI) Approve(ctx context.Context, a agent.Approval) (agent.Answer, error) {
	if u.ask != nil {
		u.ask(ctx, a)
	}
	return u.answer, nil
}

// printHi is a bash call the default policy asks about.
func printHi() llm.ToolUse {
	return llm.ToolUse{ID: "t1", Name: "bash", Input: json.RawMessage(`{"command":"printf hi"}`)}
}

func TestApprovalWaitsInTheState(t *testing.T) {
	ui := &approvingUI{answer: agent.AllowOnce}
	s := open(t, ui, reply(llm.StopToolUse, printHi()), reply(llm.StopEndTurn, llm.Text{Text: "ok"}))
	var during State
	ui.ask = func(context.Context, agent.Approval) { during = s.State() }

	if _, err := s.Prompt(context.Background(), "print"); err != nil {
		t.Fatal(err)
	}
	if len(during.Tools) != 1 || during.Tools[0].Status != StatusAwaitingApproval {
		t.Errorf("tools while asking = %+v, want t1 awaiting approval", during.Tools)
	}
	want := []string{"SessionStarted", "ToolPending", "Usage", "ToolStarted", "ToolFinished:done", "TextDelta", "Usage", "TurnDone"}
	if diff := cmp.Diff(want, ui.kinds()); diff != "" {
		t.Errorf("updates (-want +got):\n%s", diff)
	}
}

func TestDeniedApprovalFinishesTheTool(t *testing.T) {
	ui := &approvingUI{answer: agent.Deny}
	s := open(t, ui, reply(llm.StopToolUse, printHi()), reply(llm.StopEndTurn, llm.Text{Text: "ok"}))

	if _, err := s.Prompt(context.Background(), "print"); err != nil {
		t.Fatal(err)
	}
	want := []string{"SessionStarted", "ToolPending", "Usage", "ToolFinished:denied", "TextDelta", "Usage", "TurnDone"}
	if diff := cmp.Diff(want, ui.kinds()); diff != "" {
		t.Errorf("updates (-want +got):\n%s", diff)
	}
}

func TestApprovalsAreAskedOneAtATime(t *testing.T) {
	var mu sync.Mutex
	now, most := 0, 0
	ui := &approvingUI{answer: agent.AllowOnce, ask: func(context.Context, agent.Approval) {
		mu.Lock()
		now++
		most = max(most, now)
		mu.Unlock()
		time.Sleep(10 * time.Millisecond)
		mu.Lock()
		now--
		mu.Unlock()
	}}
	s := open(t, ui)

	var wg sync.WaitGroup
	for _, id := range []string{"a", "b", "c"} {
		wg.Go(func() {
			ctx := event.WithAgent(context.Background(), event.Agent{ID: id, Type: "general"})
			if _, err := s.approve(ctx, agent.Approval{Call: event.ToolCall{ToolUseID: id}}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if most != 1 {
		t.Errorf("%d approvals asked at once, want 1", most)
	}
}

func TestCancelEndsTheWaitForApproval(t *testing.T) {
	ui := &approvingUI{}
	s := open(t, ui, reply(llm.StopToolUse, printHi()))
	ui.ask = func(ctx context.Context, _ agent.Approval) {
		s.Cancel()
		<-ctx.Done()
	}

	res, err := s.Prompt(context.Background(), "print")
	if err != nil || res.Stop != agent.StopCancelled {
		t.Fatalf("Prompt = %v, %v; want cancelled", res.Stop, err)
	}
	if open := s.State().Tools; len(open) != 0 {
		t.Errorf("tools still open: %+v", open)
	}
}

func TestWithoutAnApproverTheCallIsDenied(t *testing.T) {
	ui := &recorder{}
	s := open(t, ui, reply(llm.StopToolUse, printHi()), reply(llm.StopEndTurn, llm.Text{Text: "ok"}))

	if _, err := s.Prompt(context.Background(), "print"); err != nil {
		t.Fatal(err)
	}
	for _, u := range ui.updates {
		if f, ok := u.Body.(ToolFinished); ok && f.Status != StatusDenied {
			t.Errorf("tool finished %s, want denied", f.Status)
		}
	}
}
