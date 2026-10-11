package read

import (
	"strings"
	"testing"

	"github.com/xzwache/loom/internal/tool/tooltest"
)

func TestRead(t *testing.T) {
	ws := tooltest.NewWorkspace(t, map[string]string{
		"a.txt":   "one\ntwo\nthree\n",
		"empty":   "",
		"bin.dat": "ab\x00cd",
	})
	r := &Tool{Workspace: ws, MaxOutput: 10_000}

	tests := []struct {
		name    string
		input   map[string]any
		want    string
		wantErr string
	}{
		{name: "whole file", input: map[string]any{"path": "a.txt"}, want: "     1\tone\n     2\ttwo\n     3\tthree\n"},
		{
			name:  "offset and limit",
			input: map[string]any{"path": "a.txt", "offset": 2, "limit": 1},
			want:  "     2\ttwo\n... [1 more lines; continue with offset 3]\n",
		},
		{name: "empty", input: map[string]any{"path": "empty"}, want: "(empty file)"},
		{name: "binary", input: map[string]any{"path": "bin.dat"}, wantErr: "binary"},
		{name: "missing", input: map[string]any{"path": "nope.txt"}, wantErr: "no such file"},
		{name: "offset past end", input: map[string]any{"path": "a.txt", "offset": 9}, wantErr: "past the end"},
		{name: "outside workspace", input: map[string]any{"path": "/etc/hosts"}, wantErr: "outside the workspace"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tooltest.Call(t, r, tt.input)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("read(%v) error = %v, want containing %q", tt.input, err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Errorf("read(%v) = %q, %v; want %q", tt.input, got, err, tt.want)
			}
		})
	}
}

func TestReadTruncatesLongLines(t *testing.T) {
	ws := tooltest.NewWorkspace(t, map[string]string{"long.txt": strings.Repeat("x", maxLineLength+10)})
	got, err := tooltest.Call(t, &Tool{Workspace: ws, MaxOutput: 10_000}, map[string]any{"path": "long.txt"})
	if err != nil || !strings.HasSuffix(got, "... [line truncated]\n") {
		t.Errorf("read long line = %q..., %v; want a truncation marker", got[:20], err)
	}
}
