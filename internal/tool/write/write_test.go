package write

import (
	"testing"

	"github.com/xzwache/loom/internal/tool/tooltest"
)

func TestWrite(t *testing.T) {
	ws := tooltest.NewWorkspace(t, map[string]string{"old.txt": "old"})
	w := &Tool{Workspace: ws}

	tests := []struct {
		path    string
		content string
		want    string
	}{
		{path: "new.txt", content: "hello", want: "wrote 5 bytes to new.txt"},
		{path: "a/b/c.txt", content: "nested", want: "wrote 6 bytes to a/b/c.txt"},
		{path: "old.txt", content: "replaced", want: "wrote 8 bytes to old.txt"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got, err := tooltest.Call(t, w, map[string]any{"file_path": tt.path, "content": tt.content})
			if err != nil || got != tt.want {
				t.Errorf("write(%s) = %q, %v; want %q", tt.path, got, err, tt.want)
			}
			if content := tooltest.ReadFile(t, ws, tt.path); content != tt.content {
				t.Errorf("%s = %q, want %q", tt.path, content, tt.content)
			}
		})
	}
}

func TestWriteRejects(t *testing.T) {
	w := &Tool{Workspace: tooltest.NewWorkspace(t, nil)}
	for _, input := range []map[string]any{
		{"file_path": "", "content": "x"},
		{"file_path": "../escape.txt", "content": "x"},
	} {
		if _, err := tooltest.Call(t, w, input); err == nil {
			t.Errorf("write(%v) error = nil, want error", input)
		}
	}
}
