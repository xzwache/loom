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
)

func TestRunHeadlessMemory(t *testing.T) {
	write := `{"action":"write","scope":"project","name":"tests-need-db","type":"project",` +
		`"description":"integration tests need make db","content":"Run make db before go test."}`
	extracted := `{"memories": [{"scope": "global", "name": "short-answers", "type": "feedback",` +
		` "description": "the user wants short answers", "content": "Keep answers short."}]}`
	te := newTestEnv(t, nil,
		llmtest.ToolUseReply(llm.ToolUse{ID: "t1", Name: "memory", Input: json.RawMessage(write)}),
		llmtest.TextReply("Saved."),
		llmtest.TextReply(extracted),
	)
	home := te.getenv("HOME")
	writeFile(t, userConfig(home), `{"memory": {"model": "cheap-model"}}`)
	root := filepath.Join(home, ".config", "loom", "memory")
	known := memory.Memory{Scope: memory.ScopeGlobal, Name: "go-style", Type: memory.TypeFeedback, Description: "minimal comments", Content: "Few comments."}
	if _, err := memory.NewStore(root, te.workdir).Write(known); err != nil {
		t.Fatal(err)
	}

	if code := run(context.Background(), []string{"-p", "remember that tests need make db; keep answers short"}, te.env); code != exitOK {
		t.Fatalf("run = %d; stderr: %s", code, te.stderr)
	}

	reqs := te.model.Requests()
	if len(reqs) != 3 {
		t.Fatalf("got %d requests, want 2 turns and 1 extraction", len(reqs))
	}
	if !strings.Contains(reqs[0].System, "# Memory") || !strings.Contains(reqs[0].System, "- go-style (feedback): minimal comments") {
		t.Errorf("the system prompt does not list the memories:\n%s", reqs[0].System)
	}
	if reqs[2].Model != "cheap-model" || reqs[2].Tools != nil {
		t.Errorf("extraction request = model %q, %d tools", reqs[2].Model, len(reqs[2].Tools))
	}
	store := memory.NewStore(root, te.workdir)
	for _, key := range []struct {
		scope memory.Scope
		name  string
	}{{memory.ScopeProject, "tests-need-db"}, {memory.ScopeGlobal, "short-answers"}} {
		if _, err := store.Read(key.scope, key.name); err != nil {
			t.Errorf("memory %s/%s not saved: %v", key.scope, key.name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(te.workdir, ".loom", "memory")); err == nil {
		t.Error("memory was written into the workspace")
	}
}
