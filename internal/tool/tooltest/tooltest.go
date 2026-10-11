// Package tooltest provides helpers for testing tools.
package tooltest

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/xzwache/loom/internal/tool"
)

func NewWorkspace(t *testing.T, files map[string]string) *tool.FileSystemWorkspace {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ws, err := tool.OpenFileSystemWorkspace(dir)
	if err != nil {
		t.Fatalf("OpenFileSystemWorkspace: %v", err)
	}
	t.Cleanup(func() { ws.Close() })
	return ws
}

func Call(t *testing.T, tl tool.Tool, input map[string]any) (string, error) {
	t.Helper()
	b, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return tl.Run(context.Background(), b)
}

func ReadFile(t *testing.T, ws *tool.FileSystemWorkspace, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(ws.Dir(), name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}
