package session

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/xzwache/loom/internal/llm"
)

var historyOpt = cmp.AllowUnexported(History{})

func TestStores(t *testing.T) {
	stores := map[string]func(t *testing.T) Store{
		"memory": func(*testing.T) Store { return NewMemoryStore() },
		"file":   func(t *testing.T) Store { return NewFileStore(filepath.Join(t.TempDir(), "sessions")) },
	}
	for name, newStore := range stores {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store := newStore(t)
			if _, err := Latest(ctx, store); !errors.Is(err, ErrNotFound) {
				t.Errorf("Latest on an empty store error = %v, want ErrNotFound", err)
			}

			now := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)
			older := New(now, "/w", "m")
			older.History.AddPrompt("first")
			newer := New(now.Add(time.Minute), "/w", "m")
			newer.Updated = now.Add(time.Hour)
			newer.History.Append(
				llm.Message{Role: llm.RoleUser, Content: []llm.Block{llm.Text{Text: "second"}}},
				llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{llm.ToolUse{ID: "t1", Name: "bash", Input: []byte(`{}`)}}},
				llm.Message{Role: llm.RoleUser, Content: []llm.Block{llm.ToolResult{ToolUseID: "t1", Content: "ok"}}},
			)
			for _, s := range []Session{older, newer} {
				if err := store.Save(ctx, s); err != nil {
					t.Fatalf("Save error: %v", err)
				}
			}

			got, err := store.Load(ctx, newer.ID)
			if err != nil || !cmp.Equal(got, newer, historyOpt) {
				t.Errorf("Load = %+v, %v; want %+v", got, err, newer)
			}
			got.History.AddPrompt("changed after load")
			if again, _ := store.Load(ctx, newer.ID); again.History.Len() != 3 {
				t.Errorf("changing a loaded session changed the stored one")
			}
			latest, err := Latest(ctx, store)
			if err != nil || latest.ID != newer.ID {
				t.Errorf("Latest = %s, %v; want %s", latest.ID, err, newer.ID)
			}
			if _, err := store.Load(ctx, "missing"); !errors.Is(err, ErrNotFound) {
				t.Errorf("Load missing error = %v, want ErrNotFound", err)
			}
			if err := store.Save(ctx, Session{ID: "../escape"}); err == nil {
				t.Error("Save with a path in the id: error = nil, want error")
			}
		})
	}
}

func TestFileStoreLoadRejectsPaths(t *testing.T) {
	store := NewFileStore(t.TempDir())
	if _, err := store.Load(context.Background(), "../../etc/passwd"); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("Load error = %v, want an invalid id error", err)
	}
}

func TestFileStorePermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s")
	s := New(time.Now(), "/w", "m")
	if err := NewFileStore(dir).Save(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, s.ID+".json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("session file mode = %v, %v; want 0600", info.Mode(), err)
	}
}

func TestAddPrompt(t *testing.T) {
	var h History
	h.AddPrompt("one")
	h.Append(llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{llm.Text{Text: "ok"}}})
	h.AddPrompt("two")
	h.Append(llm.Message{Role: llm.RoleUser, Content: []llm.Block{llm.ToolResult{ToolUseID: "t1"}}})
	h.AddPrompt("three")

	msgs := h.Messages()
	if len(msgs) != 4 {
		t.Fatalf("got %d messages, want 4", len(msgs))
	}
	want := []llm.Block{llm.ToolResult{ToolUseID: "t1"}, llm.Text{Text: "three"}}
	if diff := cmp.Diff(want, msgs[3].Content); diff != "" {
		t.Errorf("prompt after tool results mismatch (-want +got):\n%s", diff)
	}
}

func TestHistoryValidate(t *testing.T) {
	unanswered := []llm.Message{
		{Role: llm.RoleUser, Content: []llm.Block{llm.Text{Text: "go"}}},
		{Role: llm.RoleAssistant, Content: []llm.Block{llm.ToolUse{ID: "t1", Name: "bash"}}},
	}
	if _, err := NewHistory(unanswered); err == nil {
		t.Error("NewHistory with an unanswered tool use: error = nil, want error")
	}
	data, err := json.Marshal(unanswered)
	if err != nil {
		t.Fatal(err)
	}
	var h History
	if err := json.Unmarshal(data, &h); err == nil {
		t.Error("loading an invalid history: error = nil, want error")
	}
}
