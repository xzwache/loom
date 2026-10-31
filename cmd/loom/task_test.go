package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xzwache/loom/internal/llm"
	"github.com/xzwache/loom/internal/llm/llmtest"
	"github.com/xzwache/loom/internal/memory"
	"github.com/xzwache/loom/internal/task"
)

func createTask(id, subject string) llmtest.Reply {
	return llmtest.ToolUseReply(llm.ToolUse{ID: id, Name: "task", Input: json.RawMessage(`{"action":"create","subject":"` + subject + `"}`)})
}

func listTasks(id string) llmtest.Reply {
	return llmtest.ToolUseReply(llm.ToolUse{ID: id, Name: "task", Input: json.RawMessage(`{"action":"list"}`)})
}

func lastToolResult(t *testing.T, te *testEnv) string {
	t.Helper()
	reqs := te.model.Requests()
	msgs := reqs[len(reqs)-1].Messages
	result, ok := msgs[len(msgs)-1].Content[0].(llm.ToolResult)
	if !ok {
		t.Fatalf("the last message is not a tool result: %+v", msgs[len(msgs)-1])
	}
	return result.Content
}

func tasksRoot(te *testEnv) string {
	return filepath.Join(te.getenv("HOME"), ".local", "state", "loom", "tasks", memory.ProjectKey(te.workdir))
}

// Each session has a list of its own: the next one starts empty.
func TestRunHeadlessSessionTaskList(t *testing.T) {
	te := newTestEnv(t, nil,
		createTask("t1", "add the schema"), llmtest.TextReply("Planned."),
		listTasks("t2"), llmtest.TextReply("Nothing open."),
	)
	for _, prompt := range []string{"plan the schema work", "what is left?"} {
		if code := run(context.Background(), []string{"-p", prompt}, te.env); code != exitOK {
			t.Fatalf("run %q = %d; stderr: %s", prompt, code, te.stderr)
		}
	}
	if got := lastToolResult(t, te); got != "No open tasks." {
		t.Errorf("the second session's list = %q", got)
	}
	entries, err := os.ReadDir(tasksRoot(te))
	if err != nil || len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "session-") {
		t.Errorf("task lists = %v, %v; want the first session's", entries, err)
	}
	if _, err := os.Stat(filepath.Join(te.workdir, ".loom", "tasks")); err == nil {
		t.Error("tasks were written into the workspace")
	}
}

// A named list is shared: the next session sees its tasks, and its prompt
// says how many are open.
func TestRunHeadlessNamedTaskList(t *testing.T) {
	te := newTestEnv(t, nil,
		createTask("t1", "add the schema"), llmtest.TextReply("Planned."),
		listTasks("t2"), llmtest.TextReply("One task open."),
	)
	for _, prompt := range []string{"plan the schema work", "what is left?"} {
		if code := run(context.Background(), []string{"-tasks", "schema-work", "-p", prompt}, te.env); code != exitOK {
			t.Fatalf("run %q = %d; stderr: %s", prompt, code, te.stderr)
		}
	}
	tasks, problems := task.NewStore(filepath.Join(tasksRoot(te), "schema-work")).List()
	if len(tasks) != 1 || len(problems) > 0 {
		t.Fatalf("tasks = %v, %v", tasks, problems)
	}
	if got := lastToolResult(t, te); got != "[ ] "+tasks[0].ID+" add the schema" {
		t.Errorf("the second session's list = %q", got)
	}
	reqs := te.model.Requests()
	if strings.Contains(reqs[0].System, "# Tasks") {
		t.Error("the first session's prompt mentions tasks, but there were none")
	}
	if !strings.Contains(reqs[2].System, `The task list "schema-work" has 1 open task: 0 in progress, 1 ready, 0 waiting.`) {
		t.Errorf("the second session's prompt has no task summary:\n%s", reqs[2].System)
	}
}

func TestRunRejectsBadTaskList(t *testing.T) {
	te := newTestEnv(t, nil)
	if code := run(context.Background(), []string{"-tasks", "../x", "-p", "hi"}, te.env); code != exitError {
		t.Errorf("run = %d, want %d", code, exitError)
	}
	if !strings.Contains(te.stderr.String(), "tasks.list") {
		t.Errorf("stderr = %q", te.stderr)
	}
}
