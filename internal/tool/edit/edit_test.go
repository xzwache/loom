package edit

import (
	"strings"
	"testing"

	"github.com/xzwache/loom/internal/tool/tooltest"
)

func TestEdit(t *testing.T) {
	const original = "alpha beta gamma gamma"
	tests := []struct {
		name    string
		input   map[string]any
		want    string
		wantErr string
	}{
		{name: "unique", input: map[string]any{"old_string": "beta", "new_string": "BETA"}, want: "alpha BETA gamma gamma"},
		{name: "replace all", input: map[string]any{"old_string": "gamma", "new_string": "G", "replace_all": true}, want: "alpha beta G G"},
		{name: "not unique", input: map[string]any{"old_string": "gamma", "new_string": "G"}, wantErr: "appears 2 times"},
		{name: "not found", input: map[string]any{"old_string": "delta", "new_string": "D"}, wantErr: "not found"},
		{name: "no-op", input: map[string]any{"old_string": "beta", "new_string": "beta"}, wantErr: "the same"},
		{name: "empty old", input: map[string]any{"old_string": "", "new_string": "x"}, wantErr: "old_string is empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := tooltest.NewWorkspace(t, map[string]string{"f.txt": original})
			tt.input["file_path"] = "f.txt"
			_, err := tooltest.Call(t, &Tool{Workspace: ws}, tt.input)
			got := tooltest.ReadFile(t, ws, "f.txt")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("edit(%v) error = %v, want containing %q", tt.input, err, tt.wantErr)
				}
				if got != original {
					t.Errorf("failed edit changed the file to %q", got)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Errorf("edit(%v) = file %q, %v; want %q", tt.input, got, err, tt.want)
			}
		})
	}
}
