package task

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xzwache/loom/internal/event"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s := NewStore(filepath.Join(t.TempDir(), "tasks"))
	var tick atomic.Int64
	s.now = func() time.Time { return time.Unix(tick.Add(1), 0) }
	return s
}

func create(t *testing.T, s *Store, subject string, blockedBy ...string) Task {
	t.Helper()
	task, err := s.Create(subject, "", blockedBy)
	if err != nil {
		t.Fatalf("Create(%q): %v", subject, err)
	}
	return task
}

func TestCreateGetList(t *testing.T) {
	s := newStore(t)
	schema := create(t, s, "schema")
	api := create(t, s, "api", schema.ID)

	got, err := s.Get(api.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != Pending || got.Owner != "" || len(got.BlockedBy) != 1 || got.BlockedBy[0] != schema.ID {
		t.Errorf("Get = %+v", got)
	}
	tasks, problems := s.List()
	if len(problems) > 0 || len(tasks) != 2 || tasks[0].ID != schema.ID || tasks[1].ID != api.ID {
		t.Errorf("List = %v, %v; want schema then api", tasks, problems)
	}
}

func TestCreateRejects(t *testing.T) {
	s := newStore(t)
	tests := []struct {
		name, subject, description string
		blockedBy                  []string
		want                       string
	}{
		{"empty subject", " ", "", nil, "subject is empty"},
		{"two lines", "a\nb", "", nil, "one line"},
		{"long subject", strings.Repeat("x", MaxSubject+1), "", nil, "longer than"},
		{"long description", "a", strings.Repeat("x", MaxDescription+1), nil, "longer than"},
		{"missing blocker", "a", "", []string{"task_00000000"}, "not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := s.Create(tt.subject, tt.description, tt.blockedBy); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Create error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestEditBlockers(t *testing.T) {
	s := newStore(t)
	a, b, c := create(t, s, "a"), create(t, s, "b"), create(t, s, "c")
	if _, err := s.Edit(b.ID, Edit{AddBlockedBy: []string{a.ID}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Edit(c.ID, Edit{AddBlockedBy: []string{b.ID, b.ID}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(c.ID); len(got.BlockedBy) != 1 {
		t.Errorf("a repeated blocker is added once, got %v", got.BlockedBy)
	}

	tests := []struct {
		name, id string
		add      []string
		want     string
	}{
		{"self", a.ID, []string{a.ID}, "cannot block itself"},
		{"direct cycle", a.ID, []string{b.ID}, "cycle"},
		{"cycle through another", a.ID, []string{c.ID}, "cycle"},
		{"missing", a.ID, []string{"task_00000000"}, "not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := s.Edit(tt.id, Edit{AddBlockedBy: tt.add}); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Edit error = %v, want %q", err, tt.want)
			}
		})
	}
	if got, _ := s.Get(a.ID); len(got.BlockedBy) != 0 {
		t.Errorf("a rejected edit changed the task: %v", got.BlockedBy)
	}
}

func TestEditOnlyPending(t *testing.T) {
	s := newStore(t)
	a := create(t, s, "a")
	subject := "new subject"
	if got, err := s.Edit(a.ID, Edit{Subject: &subject}); err != nil || got.Subject != subject {
		t.Fatalf("Edit = %+v, %v", got, err)
	}
	if _, err := s.Claim(a.ID, MainOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Edit(a.ID, Edit{Subject: &subject}); err == nil {
		t.Error("Edit of an in_progress task: error = nil")
	}
}

func TestClaimCompleteUnblocks(t *testing.T) {
	s := newStore(t)
	schema := create(t, s, "schema")
	api := create(t, s, "api", schema.ID)
	docs := create(t, s, "docs", schema.ID)
	tests := create(t, s, "tests", api.ID)

	if _, err := s.Claim(api.ID, MainOwner); err == nil || !strings.Contains(err.Error(), schema.ID+" (pending)") {
		t.Errorf("Claim of a blocked task: error = %v", err)
	}
	if _, err := s.Claim(schema.ID, "agent-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(schema.ID, "agent-2"); err == nil {
		t.Error("second Claim: error = nil")
	}
	if _, _, err := s.Complete(schema.ID, "agent-2", false); err == nil {
		t.Error("Complete by another subagent: error = nil")
	}
	done, unblocked, err := s.Complete(schema.ID, "agent-1", false)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != Completed || done.Owner != "agent-1" {
		t.Errorf("completed task = %+v", done)
	}
	if len(unblocked) != 2 || unblocked[0].ID != api.ID || unblocked[1].ID != docs.ID {
		t.Errorf("unblocked = %v, want api and docs", unblocked)
	}
	if _, err := s.Claim(tests.ID, MainOwner); err == nil {
		t.Error("tests still wait on api: error = nil")
	}
	if _, _, err := s.Complete(schema.ID, "agent-1", false); err == nil {
		t.Error("Complete twice: error = nil")
	}
}

func TestMainMayFinishOrReleaseASubagentsTask(t *testing.T) {
	s := newStore(t)
	a, b := create(t, s, "a"), create(t, s, "b")
	for _, id := range []string{a.ID, b.ID} {
		if _, err := s.Claim(id, "agent-gone"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Release(a.ID, "agent-other", false); err == nil {
		t.Error("Release by another subagent: error = nil")
	}
	released, err := s.Release(a.ID, MainOwner, true)
	if err != nil || released.Status != Pending || released.Owner != "" {
		t.Errorf("Release = %+v, %v", released, err)
	}
	if _, _, err := s.Complete(b.ID, MainOwner, true); err != nil {
		t.Errorf("Complete with override: %v", err)
	}
}

func TestConcurrentClaimsOneWins(t *testing.T) {
	s := newStore(t)
	a := create(t, s, "a")
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Claim(a.ID, "agent-"+string(rune('a'+i))); err == nil {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := wins.Load(); got != 1 {
		t.Errorf("%d claims won, want 1", got)
	}
}

func TestBadIDsAndFiles(t *testing.T) {
	s := newStore(t)
	a := create(t, s, "a")
	if _, err := s.Get("../../etc/passwd"); err == nil || !strings.Contains(err.Error(), "invalid task id") {
		t.Errorf("Get of a path: error = %v", err)
	}
	if _, err := s.Get("task_00000000"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("Get of a missing task: error = %v", err)
	}

	broken := filepath.Join(s.Dir(), "task_0000beef.json")
	if err := os.WriteFile(broken, []byte(`{"id":"task_0000beef","subject":"x","status":"done"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	tasks, problems := s.List()
	if len(tasks) != 1 || len(problems) != 1 || !strings.Contains(problems[0].Error(), "unknown status") {
		t.Errorf("List = %v, %v; want a and one problem", tasks, problems)
	}
	if _, err := s.Claim(a.ID, MainOwner); err == nil || !strings.Contains(err.Error(), "unreadable tasks") {
		t.Errorf("a change with an unreadable task: error = %v", err)
	}
}

func TestToolFlow(t *testing.T) {
	s := newStore(t)
	var events []event.Event
	tl := &Tool{Store: s, Notify: func(_ context.Context, e event.Event) { events = append(events, e) }}
	run := func(ctx context.Context, in map[string]any) string {
		t.Helper()
		raw, _ := json.Marshal(in)
		out, err := tl.Run(ctx, raw)
		if err != nil {
			t.Fatalf("Run(%v): %v", in, err)
		}
		return out
	}
	ctx := context.Background()
	run(ctx, map[string]any{"action": "create", "subject": "schema"})
	tasks, _ := s.List()
	schema := tasks[0].ID
	out := run(ctx, map[string]any{"action": "create", "subject": "api", "description": "REST endpoints", "blocked_by": []string{schema}})
	tasks, _ = s.List()
	api := tasks[1].ID
	if !strings.HasPrefix(out, "Created [-] ") || !strings.Contains(out, "waiting on "+schema+" (pending)") {
		t.Errorf("create result = %q", out)
	}

	sub := event.WithAgent(ctx, event.Agent{ID: "agent-1", Type: "general-purpose"})
	run(sub, map[string]any{"action": "claim", "id": schema})
	list := run(ctx, map[string]any{"action": "list"})
	if !strings.Contains(list, "[>] "+schema+" schema (owner agent-1)") || !strings.Contains(list, "[-] ") {
		t.Errorf("list = %q", list)
	}
	out = run(sub, map[string]any{"action": "complete", "id": schema})
	if !strings.Contains(out, "Now ready:\n[ ] ") || !strings.Contains(out, " api") {
		t.Errorf("complete result = %q", out)
	}
	list = run(ctx, map[string]any{"action": "list"})
	if strings.Contains(list, "schema") || !strings.Contains(list, "(1 completed; list with status completed") {
		t.Errorf("list after complete = %q", list)
	}
	if got := run(ctx, map[string]any{"action": "get", "id": api}); !strings.HasSuffix(got, "Blocked by: "+schema+"\n\nREST endpoints") {
		t.Errorf("get = %q", got)
	}

	if len(events) != 3 {
		t.Fatalf("events = %v", events)
	}
	if done, ok := events[2].(event.TaskCompleted); !ok || done.TaskID != schema || done.Owner != "agent-1" || done.Subject != "schema" {
		t.Errorf("TaskCompleted = %+v", events[2])
	}
	if _, err := tl.Run(ctx, json.RawMessage(`{"action":"fly"}`)); err == nil {
		t.Error("unknown action: error = nil")
	}
	if _, err := tl.Run(ctx, json.RawMessage(`{"action":"create"}`)); err == nil {
		t.Error("create without subject: error = nil")
	}
}

func TestDelete(t *testing.T) {
	s := newStore(t)
	a := create(t, s, "a")
	b := create(t, s, "b", a.ID)
	if _, err := s.Delete(a.ID); err == nil || !strings.Contains(err.Error(), "blocker of "+b.ID) {
		t.Errorf("Delete of a blocker: error = %v", err)
	}
	if _, err := s.Claim(a.ID, MainOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Delete(a.ID); err == nil || !strings.Contains(err.Error(), "release it first") {
		t.Errorf("Delete in progress: error = %v", err)
	}
	if _, _, err := s.Complete(a.ID, MainOwner, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Delete(b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Delete(a.ID); err != nil {
		t.Errorf("Delete of a completed task nothing waits on: %v", err)
	}
	if tasks, _ := s.List(); len(tasks) != 0 {
		t.Errorf("List after deletes = %v", tasks)
	}
}

func TestRenderCapsAndOrders(t *testing.T) {
	s := newStore(t)
	first := create(t, s, "first")
	for i := range 40 {
		create(t, s, "waiting "+string(rune('a'+i%26)), first.ID)
	}
	ready := create(t, s, "ready")
	if _, err := s.Claim(first.ID, MainOwner); err != nil {
		t.Fatal(err)
	}
	tasks, _ := s.List()

	out := Render(tasks, "")
	lines := strings.Split(out, "\n")
	if len(lines) != ListLimit+1 {
		t.Fatalf("got %d lines, want %d tasks and a note:\n%s", len(lines), ListLimit, out)
	}
	if !strings.HasPrefix(lines[0], "[>] "+first.ID) || !strings.HasPrefix(lines[1], "[ ] "+ready.ID) {
		t.Errorf("in progress, then ready, should come first:\n%s", out)
	}
	if lines[ListLimit] != "(not shown: 12 waiting; list with a status to see them)" {
		t.Errorf("note = %q", lines[ListLimit])
	}
	if got := Render(tasks, GroupReady); got != "[ ] "+ready.ID+" ready" {
		t.Errorf("ready only = %q", got)
	}
	if got := Render(tasks, GroupCompleted); got != "No completed tasks." {
		t.Errorf("completed only = %q", got)
	}
	if got := Render(nil, ""); got != "No open tasks." {
		t.Errorf("empty = %q", got)
	}
}

func TestSummary(t *testing.T) {
	s := newStore(t)
	if got := Summary("work", nil); got != "" {
		t.Errorf("Summary of no tasks = %q", got)
	}
	a := create(t, s, "a")
	create(t, s, "b", a.ID)
	create(t, s, "c")
	if _, err := s.Claim(a.ID, MainOwner); err != nil {
		t.Fatal(err)
	}
	tasks, _ := s.List()
	if got := Summary("work", tasks); !strings.Contains(got, `"work" has 3 open tasks: 1 in progress, 1 ready, 1 waiting`) {
		t.Errorf("Summary = %q", got)
	}
}

func TestValidateListName(t *testing.T) {
	for _, name := range []string{"schema-work", "a", "v2_api"} {
		if err := ValidateListName(name); err != nil {
			t.Errorf("ValidateListName(%q) = %v", name, err)
		}
	}
	for _, name := range []string{"", "Work", "../x", "a/b", "session-1", strings.Repeat("a", 65)} {
		if err := ValidateListName(name); err == nil {
			t.Errorf("ValidateListName(%q) = nil, want error", name)
		}
	}
}

func TestPruneSessionLists(t *testing.T) {
	root := t.TempDir()
	old := time.Now().Add(-48 * time.Hour)
	for _, name := range []string{"session-old", "session-current", "session-new", "named"} {
		dir := filepath.Join(root, name)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if name != "session-new" {
			if err := os.Chtimes(dir, old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	deleted, err := PruneSessionLists(root, 24*time.Hour, time.Now(), "session-current")
	if err != nil || len(deleted) != 1 || deleted[0] != "session-old" {
		t.Errorf("PruneSessionLists = %v, %v; want only session-old", deleted, err)
	}
	for _, name := range []string{"session-current", "session-new", "named"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Errorf("%s was deleted", name)
		}
	}
	if deleted, err := PruneSessionLists(filepath.Join(root, "none"), time.Hour, time.Now(), ""); err != nil || deleted != nil {
		t.Errorf("missing root = %v, %v", deleted, err)
	}
}
