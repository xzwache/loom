package todo

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/xzwache/loom/internal/event"
)

func TestTool(t *testing.T) {
	var changed []event.TodoUpdated
	list := NewList(func(_ context.Context, items []Item) { changed = append(changed, Updated(items)) })
	tool := &Tool{List: list}

	out, err := tool.Run(context.Background(), json.RawMessage(`{"todos":[
		{"content":"read code","status":"completed"},
		{"content":"fix bug","status":"in_progress"},
		{"content":"run tests","status":"pending"}]}`))
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if want := "[x] read code\n[>] fix bug\n[ ] run tests\n"; out != want {
		t.Errorf("output = %q, want %q", out, want)
	}
	if want := []event.TodoUpdated{{Total: 3, Done: 1}}; !cmp.Equal(changed, want) {
		t.Errorf("TodoUpdated = %+v, want %+v", changed, want)
	}

	for _, bad := range []string{
		`{"todos":[{"content":"a","status":"in_progress"},{"content":"b","status":"in_progress"}]}`,
		`{"todos":[{"content":"","status":"pending"}]}`,
		`{"todos":[{"content":"a","status":"done"}]}`,
		`{"todos":"nope"}`,
	} {
		if _, err := tool.Run(context.Background(), json.RawMessage(bad)); err == nil {
			t.Errorf("Run(%s) error = nil, want error", bad)
		}
	}
	if len(list.Items()) != 3 || list.Version() != 1 {
		t.Errorf("a rejected update changed the list")
	}
}

func TestReminder(t *testing.T) {
	ctx := context.Background()
	list := NewList(nil)
	r := &Reminder{List: list, After: 2}
	turn := func() string {
		out, err := r.Hook(ctx, event.ModelRequest{})
		if err != nil {
			t.Fatal(err)
		}
		return out.AdditionalContext
	}

	for range 3 {
		if turn() != "" {
			t.Fatal("reminded about an empty list")
		}
	}
	if err := list.Set(ctx, []Item{{Content: "fix bug", Status: InProgress}}); err != nil {
		t.Fatal(err)
	}
	if got := []string{turn(), turn(), turn()}; got[0] != "" || got[1] != "" || !strings.Contains(got[2], "[>] fix bug") {
		t.Errorf("reminders = %q, want one after 2 idle turns", got)
	}
	if err := list.Set(ctx, []Item{{Content: "fix bug", Status: Completed}}); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		if turn() != "" {
			t.Fatal("reminded about a finished list")
		}
	}
}
