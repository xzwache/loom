package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/xzwache/loom/internal/llm"
	"github.com/xzwache/loom/internal/tool"
)

const (
	ActionList   = "list"
	ActionRead   = "read"
	ActionWrite  = "write"
	ActionDelete = "delete"

	ActionCreate = "create"
	ActionUpdate = "update"
)

// listNote follows every change: the list in the system prompt is rebuilt
// only at compaction, to keep the cached prompt.
const listNote = " The memory list in your system prompt is older than this change; this change replaces it."

// Tool is the memory tool. A ReadOnly tool, for subagents, can only list and
// read. OnRead and OnWrite, if set, run after each read and each change.
type Tool struct {
	Store    *Store
	ReadOnly bool
	OnRead   func(ctx context.Context, path string)
	OnWrite  func(ctx context.Context, path, action string)
}

type input struct {
	Action      string `json:"action"`
	Scope       Scope  `json:"scope"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        Type   `json:"type"`
	Content     string `json:"content"`
}

func (t *Tool) Spec() llm.ToolSpec {
	if t.ReadOnly {
		return llm.ToolSpec{
			Name:        tool.Memory,
			Description: "Read loom's memory from earlier sessions: list all memories, or read one by scope and name.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"action": {"type": "string", "enum": ["list", "read"]},
					"scope": {"type": "string", "enum": ["global", "project"]},
					"name": {"type": "string"}
				},
				"required": ["action"]
			}`),
		}
	}
	return llm.ToolSpec{
		Name: tool.Memory,
		Description: "Read and keep memories across sessions. list shows all; read returns one. write saves a memory, " +
			"replacing one with the same scope and name, so update a memory by writing it again under its name. " +
			"delete removes one that is wrong or outdated. Scope global is for every workspace (who the user is, " +
			"how they want you to work); project is for this workspace only.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"action": {"type": "string", "enum": ["list", "read", "write", "delete"]},
				"scope": {"type": "string", "enum": ["global", "project"]},
				"name": {"type": "string", "description": "lowercase-with-dashes, e.g. tests-need-db"},
				"description": {"type": "string", "description": "write: one line that says when this memory is relevant"},
				"type": {"type": "string", "enum": ["user", "feedback", "project", "reference"], "description": "write: user = who the user is; feedback = how they want you to work; project = goals and constraints not in the code; reference = where to find things"},
				"content": {"type": "string", "description": "write: the fact; for feedback and project, add why and how to apply it"}
			},
			"required": ["action"]
		}`),
	}
}

func (t *Tool) Run(ctx context.Context, raw json.RawMessage) (string, error) {
	var in input
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	switch in.Action {
	case ActionList:
		return t.list()
	case ActionRead:
		return t.read(ctx, in)
	case ActionWrite, ActionDelete:
		if t.ReadOnly {
			return "", errors.New("memory is read-only here; report what is worth remembering in your answer instead")
		}
		if in.Action == ActionWrite {
			return t.write(ctx, in)
		}
		return t.delete(ctx, in)
	default:
		return "", fmt.Errorf("unknown action %q", in.Action)
	}
}

func (t *Tool) list() (string, error) {
	mems, problems := t.Store.List()
	if len(mems) == 0 && len(problems) == 0 {
		return "No memories yet.", nil
	}
	text := Catalog(mems)
	for _, p := range problems {
		text += "\nNot readable: " + p.Error()
	}
	return text, nil
}

func (t *Tool) read(ctx context.Context, in input) (string, error) {
	m, err := t.Store.Read(in.Scope, in.Name)
	if err != nil {
		return "", err
	}
	if t.OnRead != nil {
		t.OnRead(ctx, t.Store.Path(m.Scope, m.Name))
	}
	return fmt.Sprintf("%s (%s, %s): %s\n\n%s", m.Name, m.Scope, m.Type, m.Description, m.Content), nil
}

func (t *Tool) write(ctx context.Context, in input) (string, error) {
	m := Memory{Scope: in.Scope, Name: in.Name, Description: strings.TrimSpace(in.Description), Type: in.Type, Content: in.Content}
	created, err := t.Store.Write(m)
	if err != nil {
		return "", err
	}
	action, verb := ActionUpdate, "Updated"
	if created {
		action, verb = ActionCreate, "Saved"
	}
	if t.OnWrite != nil {
		t.OnWrite(ctx, t.Store.Path(m.Scope, m.Name), action)
	}
	return fmt.Sprintf("%s %s memory %q.", verb, m.Scope, m.Name) + listNote, nil
}

func (t *Tool) delete(ctx context.Context, in input) (string, error) {
	if err := t.Store.Delete(in.Scope, in.Name); err != nil {
		return "", err
	}
	if t.OnWrite != nil {
		t.OnWrite(ctx, t.Store.Path(in.Scope, in.Name), ActionDelete)
	}
	return fmt.Sprintf("Deleted %s memory %q.", in.Scope, in.Name) + listNote, nil
}
