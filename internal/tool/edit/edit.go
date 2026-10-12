// Package edit implements the edit tool.
package edit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/xzwache/loom/internal/llm"
	"github.com/xzwache/loom/internal/tool"
)

type Tool struct {
	Workspace *tool.FileSystemWorkspace
}

var _ tool.Tool = (*Tool)(nil)

func (t *Tool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: "edit",
		Description: "Replace exact text in a file. old_string must match the file exactly, including whitespace, " +
			"and without the line-number prefix that read adds. It must be unique unless replace_all is true.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"file_path": {"type": "string", "description": "File path, relative to the working directory or absolute."},
				"old_string": {"type": "string", "description": "The exact text to replace."},
				"new_string": {"type": "string", "description": "The replacement text."},
				"replace_all": {"type": "boolean", "description": "Replace every occurrence. Default false."}
			},
			"required": ["file_path", "old_string", "new_string"],
			"additionalProperties": false
		}`),
	}
}

func (t *Tool) Run(_ context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Path       string `json:"file_path"`
		OldString  string `json:"old_string"`
		NewString  string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if in.OldString == "" {
		return "", errors.New("old_string is empty; use write to create a file")
	}
	if in.OldString == in.NewString {
		return "", errors.New("old_string and new_string are the same")
	}
	data, err := t.Workspace.ReadFile(in.Path)
	if err != nil {
		return "", err
	}

	content := string(data)
	count := strings.Count(content, in.OldString)
	switch {
	case count == 0:
		return "", fmt.Errorf("old_string not found in %s", in.Path)
	case count > 1 && !in.ReplaceAll:
		return "", fmt.Errorf("old_string appears %d times in %s; add surrounding context or set replace_all", count, in.Path)
	}
	n := 1
	if in.ReplaceAll {
		n = -1
	}
	if err := t.Workspace.WriteFile(in.Path, []byte(strings.Replace(content, in.OldString, in.NewString, n))); err != nil {
		return "", err
	}
	if !in.ReplaceAll {
		count = 1
	}
	return fmt.Sprintf("replaced %d occurrence(s) in %s", count, in.Path), nil
}
