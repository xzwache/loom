// Package todo gives the model a todo list it keeps up to date, and a hook that
// reminds it when the list goes stale.
package todo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/xzwache/loom/internal/event"
	"github.com/xzwache/loom/internal/hook"
	"github.com/xzwache/loom/internal/llm"
	"github.com/xzwache/loom/internal/tool"
)

type Status string

const (
	Pending    Status = "pending"
	InProgress Status = "in_progress"
	Completed  Status = "completed"
)

type Item struct {
	Content string `json:"content"`
	Status  Status `json:"status"`
}

// List is shared by the tool and the reminder.
type List struct {
	mu       sync.Mutex
	items    []Item
	version  int
	onChange func(ctx context.Context, items []Item)
}

// NewList returns an empty list; onChange, if set, runs after every update.
func NewList(onChange func(ctx context.Context, items []Item)) *List {
	return &List{onChange: onChange}
}

func (l *List) Items() []Item {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.items)
}

func (l *List) Version() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.version
}

func (l *List) Set(ctx context.Context, items []Item) error {
	inProgress := 0
	for i, it := range items {
		if strings.TrimSpace(it.Content) == "" {
			return fmt.Errorf("item %d has no content", i+1)
		}
		switch it.Status {
		case Pending, Completed:
		case InProgress:
			inProgress++
		default:
			return fmt.Errorf("item %d has unknown status %q", i+1, it.Status)
		}
	}
	if inProgress > 1 {
		return errors.New("only one item may be in_progress")
	}
	l.mu.Lock()
	l.items = slices.Clone(items)
	l.version++
	l.mu.Unlock()
	if l.onChange != nil {
		l.onChange(ctx, slices.Clone(items))
	}
	return nil
}

func Render(items []Item) string {
	if len(items) == 0 {
		return "(empty todo list)"
	}
	marks := map[Status]string{Pending: "[ ]", InProgress: "[>]", Completed: "[x]"}
	var b strings.Builder
	for _, it := range items {
		fmt.Fprintf(&b, "%s %s\n", marks[it.Status], it.Content)
	}
	return b.String()
}

func open(items []Item) bool {
	return slices.ContainsFunc(items, func(it Item) bool { return it.Status != Completed })
}

// Updated is the TodoUpdated event for items.
func Updated(items []Item) event.TodoUpdated {
	e := event.TodoUpdated{Total: len(items), Items: make([]event.TodoItem, 0, len(items))}
	for _, it := range items {
		if it.Status == Completed {
			e.Done++
		}
		e.Items = append(e.Items, event.TodoItem{Content: it.Content, Status: string(it.Status)})
	}
	return e
}

type Tool struct {
	List *List
}

func (t *Tool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: tool.Todo,
		Description: "Replace your todo list for the current work. Use it for tasks with several steps: " +
			"list the steps, keep exactly one in_progress, and mark each completed as soon as it is done.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"todos": {
					"type": "array",
					"items": {
						"type": "object",
						"properties": {
							"content": {"type": "string"},
							"status": {"type": "string", "enum": ["pending", "in_progress", "completed"]}
						},
						"required": ["content", "status"]
					}
				}
			},
			"required": ["todos"]
		}`),
	}
}

func (t *Tool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Todos []Item `json:"todos"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if err := t.List.Set(ctx, in.Todos); err != nil {
		return "", err
	}
	return Render(in.Todos), nil
}

// Reminder is a ModelRequest hook. When the list has open items and has not
// changed for After model requests, it reminds the model to update it.
type Reminder struct {
	List  *List
	After int

	version int
	idle    int
}

func (r *Reminder) Hook(ctx context.Context, _ event.ModelRequest) (hook.Outcome, error) {
	if _, sub := event.AgentFrom(ctx); sub {
		return hook.Outcome{}, nil
	}
	if v := r.List.Version(); v != r.version {
		r.version, r.idle = v, 0
		return hook.Outcome{}, nil
	}
	r.idle++
	items := r.List.Items()
	if r.After <= 0 || r.idle < r.After || !open(items) {
		return hook.Outcome{}, nil
	}
	r.idle = 0
	return hook.Outcome{AdditionalContext: fmt.Sprintf(
		"<system-reminder>Your todo list has not changed for %d turns. If your progress changed, update it with the todo tool. Current list:\n%s</system-reminder>",
		r.After, Render(items))}, nil
}
