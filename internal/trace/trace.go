// Package trace records every event of a run as one JSON line.
package trace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/xzwache/loom/internal/event"
	"github.com/xzwache/loom/internal/hook"
)

const redacted = "[REDACTED]"

type record struct {
	V     int         `json:"v"`
	Seq   int         `json:"seq"`
	Time  time.Time   `json:"time"`
	RunID string      `json:"run_id"`
	Event event.Name  `json:"event"`
	Data  event.Event `json:"data"`
}

// Recorder is a hook that writes each event it sees and never changes the outcome.
type Recorder struct {
	mu     sync.Mutex
	w      io.Writer
	runID  string
	seq    int
	redact *strings.Replacer
	now    func() time.Time
}

var _ hook.Hook = (*Recorder)(nil)

// New returns a Recorder that replaces every secret value with [REDACTED].
func New(w io.Writer, runID string, secrets ...string) *Recorder {
	var pairs []string
	for _, s := range secrets {
		if s != "" {
			pairs = append(pairs, s, redacted)
		}
	}
	return &Recorder{w: w, runID: runID, redact: strings.NewReplacer(pairs...), now: time.Now}
}

func (r *Recorder) Handle(_ context.Context, e event.Event) (hook.Outcome, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	line, err := json.Marshal(record{
		V:     event.SchemaVersion,
		Seq:   r.seq,
		Time:  r.now().UTC(),
		RunID: r.runID,
		Event: e.Name(),
		Data:  e,
	})
	if err != nil {
		return hook.Outcome{}, fmt.Errorf("trace: %w", err)
	}
	if _, err := io.WriteString(r.w, r.redact.Replace(string(line))+"\n"); err != nil {
		return hook.Outcome{}, fmt.Errorf("trace: %w", err)
	}
	return hook.Outcome{}, nil
}

func NewRunID(now time.Time) string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return now.UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b)
}

// Create creates dir/<runID>.jsonl, readable only by the owner.
func Create(dir, runID string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("trace: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, runID+".jsonl"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: the path is built by loom, not taken from input
	if err != nil {
		return nil, fmt.Errorf("trace: %w", err)
	}
	return f, nil
}
