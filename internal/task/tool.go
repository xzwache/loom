package task

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xzwache/loom/internal/event"
	"github.com/xzwache/loom/internal/llm"
	"github.com/xzwache/loom/internal/tool"
)

const (
	ActionCreate   = "create"
	ActionUpdate   = "update"
	ActionList     = "list"
	ActionGet      = "get"
	ActionClaim    = "claim"
	ActionComplete = "complete"
	ActionRelease  = "release"
	ActionDelete   = "delete"
)

// MainOwner is the owner name of the main agent; subagents use their id.
const MainOwner = "main"

// Tool is the task tool, shared by the main agent and subagents. Notify, if
// set, gets TaskCreated and TaskCompleted.
type Tool struct {
	Store  *Store
	Notify func(ctx context.Context, e event.Event)
}

type input struct {
	Action      string   `json:"action"`
	ID          string   `json:"id"`
	Subject     *string  `json:"subject"`
	Description *string  `json:"description"`
	BlockedBy   []string `json:"blocked_by"`
	Status      Group    `json:"status"`
}

func (t *Tool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: tool.Task,
		Description: "Keep a graph of tasks with dependencies; use the todo tool for the steps of your current work. " +
			"The list belongs to this session unless the user picked a named list that sessions share. " +
			"create adds a pending task and returns its id; blocked_by lists the ids it waits on. update changes a " +
			"pending task and adds blockers (create every task first, then link them by the returned ids). list shows " +
			"open tasks, at most 30, in progress and ready first; a status shows only that group. get shows one task " +
			"in full. claim starts a pending task whose blockers are completed and makes you its owner; complete " +
			"finishes it and names the tasks that became ready; release hands it back unfinished; delete removes a " +
			"task that is not in progress and that no unfinished task waits on.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"action": {"type": "string", "enum": ["create", "update", "list", "get", "claim", "complete", "release", "delete"]},
				"id": {"type": "string", "description": "the task, e.g. task_3f9a1c2e; for every action but create and list"},
				"subject": {"type": "string", "description": "create, update: one line, what is to be done"},
				"description": {"type": "string", "description": "create, update: details someone needs to do it without this conversation"},
				"blocked_by": {"type": "array", "items": {"type": "string"}, "description": "create, update: ids of tasks that must be completed first; update adds to the existing ones"},
				"status": {"type": "string", "enum": ["in_progress", "ready", "waiting", "completed"], "description": "list: show only this group"}
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
	owner, main := MainOwner, true
	if a, sub := event.AgentFrom(ctx); sub {
		owner, main = a.ID, false
	}
	switch in.Action {
	case ActionCreate:
		if in.Subject == nil {
			return "", fmt.Errorf("create needs a subject")
		}
		task, err := t.Store.Create(*in.Subject, deref(in.Description), in.BlockedBy)
		if err != nil {
			return "", err
		}
		t.notify(ctx, event.TaskCreated{TaskID: task.ID, Subject: task.Subject})
		return "Created " + t.line(task), nil
	case ActionUpdate:
		task, err := t.Store.Edit(in.ID, Edit{Subject: in.Subject, Description: in.Description, AddBlockedBy: in.BlockedBy})
		if err != nil {
			return "", err
		}
		return "Updated " + t.line(task), nil
	case ActionList:
		return t.list(in.Status)
	case ActionGet:
		return t.get(in.ID)
	case ActionClaim:
		task, err := t.Store.Claim(in.ID, owner)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Claimed %s (%s); you own it now.", task.ID, task.Subject), nil
	case ActionComplete:
		task, unblocked, err := t.Store.Complete(in.ID, owner, main)
		if err != nil {
			return "", err
		}
		t.notify(ctx, event.TaskCompleted{TaskID: task.ID, Subject: task.Subject, Owner: task.Owner})
		out := fmt.Sprintf("Completed %s (%s).", task.ID, task.Subject)
		if len(unblocked) > 0 {
			out += "\nNow ready:"
			for _, u := range unblocked {
				out += "\n" + line(u, nil)
			}
		}
		return out, nil
	case ActionRelease:
		task, err := t.Store.Release(in.ID, owner, main)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Released %s (%s); it is pending again.", task.ID, task.Subject), nil
	case ActionDelete:
		task, err := t.Store.Delete(in.ID)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Deleted %s (%s).", task.ID, task.Subject), nil
	default:
		return "", fmt.Errorf("unknown action %q", in.Action)
	}
}

func (t *Tool) list(group Group) (string, error) {
	switch group {
	case "", GroupInProgress, GroupReady, GroupWaiting, GroupCompleted:
	default:
		return "", fmt.Errorf("unknown status %q", group)
	}
	tasks, problems := t.Store.List()
	out := Render(tasks, group)
	for _, p := range problems {
		out += "\nNot readable: " + p.Error()
	}
	return out, nil
}

func (t *Tool) get(id string) (string, error) {
	task, err := t.Store.Get(id)
	if err != nil {
		return "", err
	}
	out := t.line(task)
	if len(task.BlockedBy) > 0 {
		out += "\nBlocked by: " + strings.Join(task.BlockedBy, ", ")
	}
	if task.Description != "" {
		out += "\n\n" + task.Description
	}
	return out, nil
}

// line is one task with what it waits on now.
func (t *Tool) line(task Task) string {
	tasks, _ := t.Store.List()
	return line(task, waitingOn(byID(tasks), task))
}

func (t *Tool) notify(ctx context.Context, e event.Event) {
	if t.Notify != nil {
		t.Notify(ctx, e)
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
