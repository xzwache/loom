package trace

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xzwache/loom/internal/event"
)

func TestRecorder(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, "run-1", "sk-secret", "")
	r.now = func() time.Time { return time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC) }

	for _, e := range []event.Event{
		event.UserPromptSubmit{Prompt: "use key sk-secret"},
		event.SessionEnd{Reason: "done"},
	} {
		if _, err := r.Handle(context.Background(), e); err != nil {
			t.Fatalf("Handle(%s) error: %v", e.Name(), err)
		}
	}

	want := `{"v":1,"seq":1,"time":"2026-10-12T10:00:00Z","run_id":"run-1","event":"UserPromptSubmit","data":{"prompt":"use key [REDACTED]"}}
{"v":1,"seq":2,"time":"2026-10-12T10:00:00Z","run_id":"run-1","event":"SessionEnd","data":{"reason":"done"}}
`
	if got := buf.String(); got != want {
		t.Errorf("trace =\n%s\nwant\n%s", got, want)
	}
}

func TestCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runs")
	f, err := Create(dir, "run-1")
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	defer f.Close()
	info, err := os.Stat(filepath.Join(dir, "run-1.jsonl"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("trace file mode = %v, %v; want 0600", info.Mode(), err)
	}
	if _, err := Create(dir, "run-1"); err == nil {
		t.Error("Create existing run: error = nil, want error")
	}
}
