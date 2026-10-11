package tool_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/xzwache/loom/internal/tool"
	"github.com/xzwache/loom/internal/tool/tooltest"
)

func TestWorkspaceConfinement(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws := tooltest.NewWorkspace(t, map[string]string{"ok.txt": "fine"})
	if err := os.Symlink(outside, filepath.Join(ws.Dir(), "link")); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		"../" + filepath.Base(outside) + "/secret.txt",
		filepath.Join(outside, "secret.txt"),
		"link/secret.txt",
	} {
		if _, err := ws.ReadFile(path); err == nil {
			t.Errorf("ReadFile(%q) error = nil, want escape rejected", path)
		}
		if err := ws.WriteFile(path, []byte("x")); err == nil {
			t.Errorf("WriteFile(%q) error = nil, want escape rejected", path)
		}
	}
	if got, err := ws.ReadFile(filepath.Join(ws.Dir(), "ok.txt")); err != nil || string(got) != "fine" {
		t.Errorf("ReadFile(absolute path inside) = %q, %v; want fine", got, err)
	}
	if _, err := ws.ToRelativePath("/elsewhere/x"); !errors.Is(err, tool.ErrOutsideWorkspace) {
		t.Errorf("ToRelativePath(outside) error = %v, want ErrOutsideWorkspace", err)
	}
}

func TestWorkspaceWriteFile(t *testing.T) {
	ws := tooltest.NewWorkspace(t, map[string]string{"script.sh": "old", "dir/x": ""})
	if err := os.Chmod(filepath.Join(ws.Dir(), "script.sh"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := ws.WriteFile("deep/new/file.txt", []byte("hello")); err != nil {
		t.Fatalf("WriteFile new nested file: %v", err)
	}
	if got := tooltest.ReadFile(t, ws, "deep/new/file.txt"); got != "hello" {
		t.Errorf("new file = %q, want hello", got)
	}

	if err := ws.WriteFile("script.sh", []byte("new")); err != nil {
		t.Fatalf("WriteFile overwrite: %v", err)
	}
	info, err := os.Stat(filepath.Join(ws.Dir(), "script.sh"))
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Errorf("overwritten file mode = %v, %v; want 0755 kept", info.Mode(), err)
	}

	if err := ws.WriteFile("dir", []byte("x")); err == nil {
		t.Error("WriteFile over a directory: error = nil, want error")
	}
	if err := ws.WriteFile("", []byte("x")); err == nil {
		t.Error("WriteFile empty path: error = nil, want error")
	}
}
