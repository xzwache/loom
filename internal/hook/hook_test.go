package hook_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/xzwache/loom/internal/event"
	"github.com/xzwache/loom/internal/hook"
)

func pre(input string) event.PreToolUse {
	return event.PreToolUse{ToolCall: event.ToolCall{ToolUseID: "t1", ToolName: "bash", ToolInput: json.RawMessage(input)}}
}

func TestFireRunsMatchingHooksInOrder(t *testing.T) {
	var got []string
	c := &hook.Pipeline{}
	c.OnAll(hook.Func(func(_ context.Context, e event.Event) (hook.Outcome, error) {
		got = append(got, "all:"+string(e.Name()))
		return hook.Outcome{}, nil
	}))
	hook.On(c, func(_ context.Context, e event.PreToolUse) (hook.Outcome, error) {
		got = append(got, "pre:"+e.ToolName)
		return hook.Outcome{}, nil
	})
	hook.On(c, func(context.Context, event.Stop) (hook.Outcome, error) {
		got = append(got, "stop")
		return hook.Outcome{}, nil
	})

	if _, err := c.Fire(context.Background(), pre(`{}`)); err != nil {
		t.Fatalf("Fire error: %v", err)
	}
	if want := []string{"all:PreToolUse", "pre:bash"}; !cmp.Equal(got, want) {
		t.Errorf("hooks ran %v, want %v", got, want)
	}
}

func TestFireMergesOutcomes(t *testing.T) {
	c := &hook.Pipeline{}
	hook.On(c, func(context.Context, event.PreToolUse) (hook.Outcome, error) {
		return hook.Outcome{Decision: hook.Allow, UpdatedInput: json.RawMessage(`{"command":"ls"}`)}, nil
	})
	var seen string
	hook.On(c, func(_ context.Context, e event.PreToolUse) (hook.Outcome, error) {
		seen = string(e.ToolInput)
		return hook.Outcome{Decision: hook.Ask, Reason: "check"}, nil
	})

	out, err := c.Fire(context.Background(), pre(`{"command":"rm"}`))
	if err != nil {
		t.Fatalf("Fire error: %v", err)
	}
	if seen != `{"command":"ls"}` {
		t.Errorf("second hook saw input %s, want the rewritten one", seen)
	}
	want := hook.Outcome{Decision: hook.Ask, Reason: "check", UpdatedInput: json.RawMessage(`{"command":"ls"}`)}
	if diff := cmp.Diff(want, out); diff != "" {
		t.Errorf("outcome mismatch (-want +got):\n%s", diff)
	}
}

func TestFireStopsAtDenyAndHalt(t *testing.T) {
	for _, first := range []hook.Outcome{{Decision: hook.Deny, Reason: "no"}, {Halt: true, Reason: "stop"}} {
		c := &hook.Pipeline{}
		hook.On(c, func(context.Context, event.PreToolUse) (hook.Outcome, error) { return first, nil })
		ran := false
		hook.On(c, func(context.Context, event.PreToolUse) (hook.Outcome, error) {
			ran = true
			return hook.Outcome{Decision: hook.Allow}, nil
		})

		out, _ := c.Fire(context.Background(), pre(`{}`))
		if ran || out.Decision != first.Decision || out.Halt != first.Halt {
			t.Errorf("after %+v: later hook ran %v, outcome %+v", first, ran, out)
		}
	}
}

func TestFireSkipsFailingHooks(t *testing.T) {
	c := &hook.Pipeline{}
	hook.On(c, func(context.Context, event.Stop) (hook.Outcome, error) { return hook.Outcome{}, errors.New("boom") })
	hook.On(c, func(context.Context, event.Stop) (hook.Outcome, error) { panic("bug") })
	hook.On(c, func(context.Context, event.Stop) (hook.Outcome, error) { return hook.Outcome{Block: true}, nil })

	out, err := c.Fire(context.Background(), event.Stop{})
	if !out.Block {
		t.Errorf("outcome = %+v, want the working hook's block", out)
	}
	if err == nil || !strings.Contains(err.Error(), "boom") || !strings.Contains(err.Error(), "panicked: bug") {
		t.Errorf("Fire error = %v, want both failures", err)
	}
}

func TestNilChain(t *testing.T) {
	var c *hook.Pipeline
	if out, err := c.Fire(context.Background(), event.Stop{}); err != nil || !cmp.Equal(out, hook.Outcome{}) {
		t.Errorf("nil chain Fire = %+v, %v; want zero", out, err)
	}
}

func TestObserversSeeTheFinalEvent(t *testing.T) {
	c := &hook.Pipeline{}
	hook.On(c, func(context.Context, event.PreToolUse) (hook.Outcome, error) {
		return hook.Outcome{UpdatedInput: json.RawMessage(`{"command":"ls"}`)}, nil
	})
	hook.On(c, func(context.Context, event.PreToolUse) (hook.Outcome, error) {
		return hook.Outcome{Decision: hook.Deny}, nil
	})
	var seen []string
	c.Observe(func(_ context.Context, e event.Event) {
		call, _ := event.Tool(e)
		seen = append(seen, string(e.Name())+" "+string(call.ToolInput))
	})

	if _, err := c.Fire(context.Background(), pre(`{"command":"rm"}`)); err != nil {
		t.Fatal(err)
	}
	if want := []string{`PreToolUse {"command":"ls"}`}; !cmp.Equal(seen, want) {
		t.Errorf("observer saw %v, want %v", seen, want)
	}
}

func TestShowSkipsHooks(t *testing.T) {
	c := &hook.Pipeline{}
	hookRan := false
	c.OnAll(hook.Func(func(context.Context, event.Event) (hook.Outcome, error) {
		hookRan = true
		return hook.Outcome{}, nil
	}))
	c.Observe(func(context.Context, event.Event) { panic("bug") })
	var shown []event.Event
	c.Observe(func(_ context.Context, e event.Event) { shown = append(shown, e) })

	c.Show(context.Background(), event.ModelTextDelta{Text: "hi"})
	if hookRan {
		t.Error("a hook ran on a shown event")
	}
	if want := []event.Event{event.ModelTextDelta{Text: "hi"}}; !cmp.Equal(shown, want) {
		t.Errorf("shown %v, want %v (a panicking observer must not stop the others)", shown, want)
	}
	var nilPipeline *hook.Pipeline
	nilPipeline.Show(context.Background(), event.ModelTextDelta{})
}
