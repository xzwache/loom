package hook_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/xzwache/loom/internal/event"
	"github.com/xzwache/loom/internal/hook"
)

func TestFireDropsAnswersTheEventDoesNotAccept(t *testing.T) {
	var logs bytes.Buffer
	p := &hook.Pipeline{Log: slog.New(slog.NewTextHandler(&logs, nil))}
	hook.On(p, func(context.Context, event.SessionStart) (hook.Outcome, error) {
		return hook.Outcome{Decision: hook.Deny, Block: true, AdditionalContext: "hello"}, nil
	})
	hook.On(p, func(context.Context, event.SessionStart) (hook.Outcome, error) {
		return hook.Outcome{AdditionalContext: "world"}, nil
	})

	out, err := p.Fire(context.Background(), event.SessionStart{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision != "" || out.Block || out.AdditionalContext != "hello\nworld" {
		t.Errorf("outcome = %+v, want only the context", out)
	}
	for _, want := range []string{"field=decision", "field=block"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs lack %s:\n%s", want, logs.String())
		}
	}
}

func TestFireKeepsAcceptedAnswers(t *testing.T) {
	p := &hook.Pipeline{}
	hook.On(p, func(context.Context, event.PreToolUse) (hook.Outcome, error) {
		return hook.Outcome{Decision: hook.Ask, UpdatedInput: json.RawMessage(`{"a":1}`), Reason: "check"}, nil
	})

	out, err := p.Fire(context.Background(), pre(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision != hook.Ask || string(out.UpdatedInput) != `{"a":1}` || out.Reason != "check" {
		t.Errorf("outcome = %+v", out)
	}
}

func TestFireDropsDenyOnNoticeWithoutStopping(t *testing.T) {
	p := &hook.Pipeline{}
	ran := false
	hook.On(p, func(context.Context, event.SkillLoaded) (hook.Outcome, error) {
		return hook.Outcome{Decision: hook.Deny}, nil
	})
	hook.On(p, func(context.Context, event.SkillLoaded) (hook.Outcome, error) {
		ran = true
		return hook.Outcome{}, nil
	})

	if _, err := p.Fire(context.Background(), event.SkillLoaded{}); err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Error("an ignored deny stopped the chain")
	}
}

func TestNotifyLogsFailures(t *testing.T) {
	var logs bytes.Buffer
	p := &hook.Pipeline{Log: slog.New(slog.NewTextHandler(&logs, nil))}
	hook.On(p, func(context.Context, event.SkillUsed) (hook.Outcome, error) {
		return hook.Outcome{}, errors.New("broken script")
	})

	p.Notify(context.Background(), event.SkillUsed{})
	if !strings.Contains(logs.String(), "broken script") {
		t.Errorf("logs lack the failure:\n%s", logs.String())
	}
	var nilPipeline *hook.Pipeline
	nilPipeline.Notify(context.Background(), event.SkillUsed{})
}
