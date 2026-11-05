package frontend

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/xzwache/loom/internal/agent"
	"github.com/xzwache/loom/internal/llm"
	"github.com/xzwache/loom/internal/llm/llmtest"
)

func TestParseCommand(t *testing.T) {
	tests := []struct {
		text, name, args string
		ok               bool
	}{
		{"/help", "help", "", true},
		{"  /compact  keep the plan ", "compact", "keep the plan", true},
		{"/usr/bin is empty", "", "", false},
		{"/", "", "", false},
		{"/Help", "", "", false},
		{"fix /etc/hosts", "", "", false},
	}
	for _, tt := range tests {
		name, args, ok := parseCommand(tt.text)
		if name != tt.name || args != tt.args || ok != tt.ok {
			t.Errorf("parseCommand(%q) = %q, %q, %v; want %q, %q, %v", tt.text, name, args, ok, tt.name, tt.args, tt.ok)
		}
	}
}

// notices are the messages of the Notice updates, in order.
func (r *recorder) notices() []string {
	var out []string
	for _, u := range r.updates {
		if n, ok := u.Body.(Notice); ok {
			out = append(out, n.Message)
		}
	}
	return out
}

func TestCommandsDoNotCallTheModel(t *testing.T) {
	ui := &recorder{}
	s := open(t, ui) // no replies: a model call fails the prompt

	for _, text := range []string{"/help", "/nope"} {
		if res, err := s.Prompt(context.Background(), text); err != nil || res.Stop != agent.StopDone {
			t.Errorf("Prompt(%q) = %v, %v", text, res.Stop, err)
		}
	}
	got := ui.notices()
	if len(got) != 2 || !strings.Contains(got[0], "/compact  compact the conversation now") || !strings.Contains(got[1], "unknown command /nope") {
		t.Errorf("notices = %q", got)
	}
}

func TestPathIsAPromptNotACommand(t *testing.T) {
	s := open(t, &recorder{}, llmtest.TextReply("it is"))
	if res, err := s.Prompt(context.Background(), "/usr/bin is empty?"); err != nil || res.Text != "it is" {
		t.Errorf("Prompt = %+v, %v; want the model's answer", res, err)
	}
}

func TestExitEndsTheSession(t *testing.T) {
	ui := &recorder{}
	s := open(t, ui)

	if _, err := s.Prompt(context.Background(), "/exit"); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"SessionStarted", "SessionEnded", "TurnDone"}, ui.kinds()); diff != "" {
		t.Errorf("updates (-want +got):\n%s", diff)
	}
	if end := ui.updates[1].Body.(SessionEnded); end.Reason != EndExit {
		t.Errorf("end reason = %q, want %q", end.Reason, EndExit)
	}
	if _, err := s.Prompt(context.Background(), "hello?"); !errors.Is(err, ErrClosed) {
		t.Errorf("Prompt after /exit: %v, want ErrClosed", err)
	}
}

func TestClearStartsAFreshSession(t *testing.T) {
	ui := &recorder{}
	s := open(t, ui, llmtest.TextReply("hi"), llmtest.TextReply("fresh"))
	if _, err := s.Prompt(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	before := s.conversation.Session.ID

	ui.updates = nil
	if _, err := s.Prompt(context.Background(), "/clear"); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"SessionEnded", "SessionStarted", "TurnDone"}, ui.kinds()); diff != "" {
		t.Errorf("updates (-want +got):\n%s", diff)
	}
	if s.conversation.Session.ID == before || len(s.conversation.Session.History.Messages()) != 0 {
		t.Errorf("after /clear: session %s with %d messages, want a new empty one", s.conversation.Session.ID, len(s.conversation.Session.History.Messages()))
	}
	if res, err := s.Prompt(context.Background(), "again"); err != nil || res.Text != "fresh" {
		t.Errorf("prompt after /clear = %+v, %v", res, err)
	}
}

func TestCompactNow(t *testing.T) {
	ui := &recorder{}
	s := open(t, ui, llmtest.TextReply("hi"), reply(llm.StopEndTurn, llm.Text{Text: "Task: say hi."}))

	if _, err := s.Prompt(context.Background(), "/compact"); err != nil {
		t.Fatal(err)
	}
	if got := ui.notices(); !cmp.Equal(got, []string{"nothing to compact"}) {
		t.Errorf("notices on an empty session = %q", got)
	}

	if _, err := s.Prompt(context.Background(), "say hi"); err != nil {
		t.Fatal(err)
	}
	ui.updates = nil
	res, err := s.Prompt(context.Background(), "/compact")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(ui.kinds(), " "), "Compacted") {
		t.Errorf("updates = %v, want a Compacted", ui.kinds())
	}
	if res.Usage == (llm.Usage{}) {
		t.Error("the summary's tokens are not in the result")
	}
	msgs := s.conversation.Session.History.Messages()
	if len(msgs) != 1 || !strings.Contains(text(msgs[0].Content), "Task: say hi.") {
		t.Errorf("history after /compact = %+v", msgs)
	}
}

func text(blocks []llm.Block) string {
	var b strings.Builder
	for _, bl := range blocks {
		if t, ok := bl.(llm.Text); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}
