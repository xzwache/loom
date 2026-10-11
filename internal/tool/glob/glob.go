// Package glob implements the glob tool.
package glob

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/xzwache/loom/internal/llm"
	"github.com/xzwache/loom/internal/tool"
)

const defaultMaxResults = 200

var skipDirs = map[string]bool{".git": true, "node_modules": true}

type Tool struct {
	Workspace  *tool.FileSystemWorkspace
	MaxResults int
}

var _ tool.Tool = (*Tool)(nil)

func (t *Tool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: "glob",
		Description: "Find files by pattern, such as \"**/*.go\" or \"cmd/*/main.go\". " +
			"Returns paths relative to the working directory, most recently modified first. Skips .git and node_modules.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"pattern": {"type": "string", "description": "Glob pattern; ** matches any number of directories."}
			},
			"required": ["pattern"],
			"additionalProperties": false
		}`),
	}
}

type match struct {
	path    string
	modTime time.Time
}

func (t *Tool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Pattern string `json:"pattern"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	pattern, err := t.Workspace.ToRelativePath(in.Pattern)
	if err != nil {
		return "", err
	}
	if !doublestar.ValidatePattern(pattern) {
		return "", fmt.Errorf("invalid pattern %q", in.Pattern)
	}

	matches, err := walk(ctx, t.Workspace.FS(), pattern)
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "no files match " + in.Pattern, nil
	}
	slices.SortFunc(matches, newestFirst)

	limit := t.MaxResults
	if limit <= 0 {
		limit = defaultMaxResults
	}
	var b strings.Builder
	for _, m := range matches[:min(limit, len(matches))] {
		b.WriteString(m.path)
		b.WriteByte('\n')
	}
	if len(matches) > limit {
		fmt.Fprintf(&b, "... [%d more files; use a narrower pattern]\n", len(matches)-limit)
	}
	return b.String(), nil
}

func walk(ctx context.Context, fsys fs.FS, pattern string) ([]match, error) {
	var matches []match
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			return nil //nolint:nilerr // skip unreadable entries instead of failing the search
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if ok, _ := doublestar.Match(pattern, path); !ok {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil //nolint:nilerr // the file vanished while walking
		}
		matches = append(matches, match{path: path, modTime: info.ModTime()})
		return nil
	})
	return matches, err
}

func newestFirst(a, b match) int {
	if c := b.modTime.Compare(a.modTime); c != 0 {
		return c
	}
	return strings.Compare(a.path, b.path)
}
