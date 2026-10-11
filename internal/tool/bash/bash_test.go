//go:build unix

package bash

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func runBash(t *testing.T, ctx context.Context, b *Tool, command string) (string, error) {
	t.Helper()
	input, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		t.Fatal(err)
	}
	return b.Run(ctx, input)
}

func TestBash(t *testing.T) {
	dir := t.TempDir()
	b := &Tool{Dir: dir, Timeout: 5 * time.Second, MaxOutput: 1000}

	tests := []struct {
		name    string
		command string
		want    string
		wantErr string
	}{
		{name: "stdout", command: "echo hello", want: "hello\n"},
		{name: "stderr is captured", command: "echo oops >&2", want: "oops\n"},
		{name: "runs in dir", command: "pwd", want: dir},
		{name: "no output", command: "true", want: "(no output)"},
		{name: "non-zero exit", command: "echo bad; exit 3", wantErr: "exit code 3\nbad"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := runBash(t, context.Background(), b, tt.command)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("Run(%q) error = %v, want containing %q", tt.command, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Run(%q) error: %v", tt.command, err)
			}
			if !strings.Contains(got, tt.want) {
				t.Errorf("Run(%q) = %q, want containing %q", tt.command, got, tt.want)
			}
		})
	}
}

func TestBashInvalidInput(t *testing.T) {
	b := &Tool{Timeout: time.Second, MaxOutput: 100}
	for _, input := range []string{`{`, `{"command": ""}`} {
		if _, err := b.Run(context.Background(), json.RawMessage(input)); err == nil {
			t.Errorf("Run(%s) error = nil, want error", input)
		}
	}
}

func TestBashTruncatesOutput(t *testing.T) {
	b := &Tool{Timeout: 5 * time.Second, MaxOutput: 100}
	got, err := runBash(t, context.Background(), b, "head -c 5000 /dev/zero | tr '\\0' a")
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if !strings.Contains(got, "[4900 bytes truncated]") || len(got) > 200 {
		t.Errorf("Run output (%d bytes) = %q, want truncated", len(got), got)
	}
}

func TestBashTimeoutKillsChildren(t *testing.T) {
	b := &Tool{Timeout: 200 * time.Millisecond, MaxOutput: 100}
	start := time.Now()
	_, err := runBash(t, context.Background(), b, "sleep 30 & sleep 30; wait")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("Run error = %v, want timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("Run took %s after timeout, want the process group killed promptly", elapsed)
	}
}

func TestBashCancelled(t *testing.T) {
	b := &Tool{Timeout: 10 * time.Second, MaxOutput: 100}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := runBash(t, ctx, b, "sleep 30")
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Errorf("Run error = %v, want cancelled", err)
	}
}
