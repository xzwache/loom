// Package write implements the write tool.
package write

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/xzwache/loom/internal/llm"
	"github.com/xzwache/loom/internal/tool"
)

type Tool struct {
	Workspace *tool.FileSystemWorkspace
}

var _ tool.Tool = (*Tool)(nil)

func (t *Tool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        tool.Write,
		Description: "Create or overwrite a file with the given content. Missing parent directories are created.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"file_path": {"type": "string", "description": "File path, relative to the working directory or absolute."},
				"content": {"type": "string", "description": "The complete new content of the file."}
			},
			"required": ["file_path", "content"],
			"additionalProperties": false
		}`),
	}
}

func (t *Tool) Run(_ context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Path    string `json:"file_path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if err := t.Workspace.WriteFile(in.Path, []byte(in.Content)); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(in.Content), in.Path), nil
}
