// Package read implements the read tool.
package read

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xzwache/loom/internal/llm"
	"github.com/xzwache/loom/internal/tool"
)

const (
	defaultLines  = 2000
	maxLineLength = 2000
)

type Tool struct {
	Workspace *tool.FileSystemWorkspace
	MaxOutput int
}

var _ tool.Tool = (*Tool)(nil)

func (t *Tool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: "read",
		Description: "Read a text file. Lines are returned as \"<line number>\\t<text>\". " +
			"Use offset and limit to read part of a large file.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"path": {"type": "string", "description": "File path, relative to the working directory or absolute."},
				"offset": {"type": "integer", "minimum": 1, "description": "First line to read, starting at 1."},
				"limit": {"type": "integer", "minimum": 1, "description": "Number of lines to read. Default 2000."}
			},
			"required": ["path"],
			"additionalProperties": false
		}`),
	}
}

func (t *Tool) Run(_ context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	data, err := t.Workspace.ReadFile(in.Path)
	if err != nil {
		return "", err
	}
	if bytes.IndexByte(data[:min(len(data), 8192)], 0) >= 0 {
		return "", fmt.Errorf("%s looks like a binary file", in.Path)
	}
	if len(data) == 0 {
		return "(empty file)", nil
	}

	offset, limit := max(in.Offset, 1), in.Limit
	if limit <= 0 {
		limit = defaultLines
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if offset > len(lines) {
		return "", fmt.Errorf("offset %d is past the end of the file (%d lines)", offset, len(lines))
	}
	end := min(offset-1+limit, len(lines))

	var b strings.Builder
	for i := offset - 1; i < end; i++ {
		line := lines[i]
		if len(line) > maxLineLength {
			line = strings.ToValidUTF8(line[:maxLineLength], "") + "... [line truncated]"
		}
		fmt.Fprintf(&b, "%6d\t%s\n", i+1, line)
	}
	if end < len(lines) {
		fmt.Fprintf(&b, "... [%d more lines; continue with offset %d]\n", len(lines)-end, end+1)
	}
	return tool.Truncate(b.String(), t.MaxOutput), nil
}
