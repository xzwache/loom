package glob

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xzwache/loom/internal/tool/tooltest"
)

func TestGlob(t *testing.T) {
	ws := tooltest.NewWorkspace(t, map[string]string{
		"main.go":                  "",
		"cmd/loom/main.go":         "",
		"internal/a/a.go":          "",
		"internal/a/a_test.go":     "",
		"README.md":                "",
		".git/hooks/pre-commit.go": "",
		"node_modules/x/index.go":  "",
	})
	now := time.Now()
	for _, name := range []string{"cmd/loom/main.go", "internal/a/a.go", "internal/a/a_test.go", "main.go"} {
		mtime := now
		if name == "main.go" {
			mtime = now.Add(-time.Hour)
		}
		if err := os.Chtimes(filepath.Join(ws.Dir(), name), mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	g := &Tool{Workspace: ws}

	tests := []struct {
		pattern string
		want    string
	}{
		{pattern: "**/*.go", want: "cmd/loom/main.go\ninternal/a/a.go\ninternal/a/a_test.go\nmain.go\n"},
		{pattern: "internal/**/*_test.go", want: "internal/a/a_test.go\n"},
		{pattern: "*.md", want: "README.md\n"},
		{pattern: "*.rs", want: "no files match *.rs"},
	}
	for _, tt := range tests {
		t.Run(tt.pattern, func(t *testing.T) {
			got, err := tooltest.Call(t, g, map[string]any{"pattern": tt.pattern})
			if err != nil || got != tt.want {
				t.Errorf("glob(%q) = %q, %v; want %q", tt.pattern, got, err, tt.want)
			}
		})
	}
}

func TestGlobLimitAndErrors(t *testing.T) {
	ws := tooltest.NewWorkspace(t, map[string]string{"a.go": "", "b.go": "", "c.go": ""})

	got, err := tooltest.Call(t, &Tool{Workspace: ws, MaxResults: 1}, map[string]any{"pattern": "*.go"})
	if err != nil || !strings.Contains(got, "2 more files") {
		t.Errorf("glob with limit 1 = %q, %v; want a truncation note", got, err)
	}
	if _, err := tooltest.Call(t, &Tool{Workspace: ws}, map[string]any{"pattern": "["}); err == nil {
		t.Error("glob invalid pattern: error = nil, want error")
	}
}
