package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xzwache/loom/internal/config"
	"github.com/xzwache/loom/internal/llm"
	"github.com/xzwache/loom/internal/llm/llmtest"
)

type testEnv struct {
	env
	stdout *bytes.Buffer
	stderr *bytes.Buffer
	model  *llmtest.Scripted
}

func newTestEnv(t *testing.T, vars map[string]string, replies ...llmtest.Reply) *testEnv {
	t.Helper()
	if vars == nil {
		vars = map[string]string{"ANTHROPIC_API_KEY": "sk-secret", "LOOM_MODEL": "test-model"}
	}
	vars["HOME"] = t.TempDir()
	te := &testEnv{stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, model: llmtest.NewScripted(replies...)}
	te.env = env{
		stdout:   te.stdout,
		stderr:   te.stderr,
		getenv:   func(k string) string { return vars[k] },
		workdir:  t.TempDir(),
		newModel: func(config.Config) llm.Model { return te.model },
	}
	return te
}

func TestRun(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte(`{"max_turns": -1}`), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		args       []string
		vars       map[string]string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{name: "version", args: []string{"-version"}, wantCode: exitOK, wantStdout: "loom dev"},
		{name: "print config", args: []string{"-print-config"}, wantCode: exitOK, wantStdout: `"max_turns": 50`},
		{name: "invalid config file", args: []string{"-config", bad, "-p", "hi"}, wantCode: exitError, wantStderr: "max_turns must be positive"},
		{name: "unknown flag", args: []string{"-nope"}, wantCode: exitUsage, wantStderr: "flag provided but not defined"},
		{name: "no task", args: nil, wantCode: exitUsage, wantStderr: "no task given"},
		{
			name:       "missing api key",
			args:       []string{"-p", "hi"},
			vars:       map[string]string{"LOOM_MODEL": "test-model"},
			wantCode:   exitError,
			wantStderr: "ANTHROPIC_API_KEY is not set",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			te := newTestEnv(t, tt.vars)
			code := run(context.Background(), tt.args, te.env)
			if code != tt.wantCode {
				t.Errorf("run(%q) = %d, want %d; stderr: %s", tt.args, code, tt.wantCode, te.stderr)
			}
			if !strings.Contains(te.stdout.String(), tt.wantStdout) {
				t.Errorf("run(%q) stdout = %q, want containing %q", tt.args, te.stdout, tt.wantStdout)
			}
			if !strings.Contains(te.stderr.String(), tt.wantStderr) {
				t.Errorf("run(%q) stderr = %q, want containing %q", tt.args, te.stderr, tt.wantStderr)
			}
			if strings.Contains(te.stdout.String()+te.stderr.String(), "sk-secret") {
				t.Errorf("run(%q) leaked the API key", tt.args)
			}
		})
	}
}

func TestRunTask(t *testing.T) {
	te := newTestEnv(t, nil,
		llmtest.ToolUseReply(llm.ToolUse{ID: "t1", Name: "bash", Input: json.RawMessage(`{"command":"echo hi > out.txt"}`)}),
		llmtest.TextReply("Created out.txt."),
	)

	code := run(context.Background(), []string{"-p", "create out.txt"}, te.env)
	if code != exitOK {
		t.Fatalf("run = %d, want %d; stderr: %s", code, exitOK, te.stderr)
	}
	if got := te.stdout.String(); got != "Created out.txt.\n" {
		t.Errorf("stdout = %q, want the final answer", got)
	}
	data, err := os.ReadFile(filepath.Join(te.workdir, "out.txt"))
	if err != nil || string(data) != "hi\n" {
		t.Errorf("out.txt = %q, %v; want the bash tool to have run in the workdir", data, err)
	}
	req := te.model.Requests()[0]
	if req.Model != "test-model" || !strings.Contains(req.System, te.workdir) || len(req.Tools) != 5 {
		t.Errorf("first request = model %q, system %q, %d tools", req.Model, req.System, len(req.Tools))
	}
}

func TestRunTaskJSON(t *testing.T) {
	te := newTestEnv(t, nil, llmtest.TextReply("done"))

	if code := run(context.Background(), []string{"-p", "hi", "-json", "-quiet"}, te.env); code != exitOK {
		t.Fatalf("run = %d, want %d; stderr: %s", code, exitOK, te.stderr)
	}
	var out taskOutput
	if err := json.Unmarshal(te.stdout.Bytes(), &out); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, te.stdout)
	}
	if out.Stop != "done" || out.Text != "done" || out.Turns != 1 {
		t.Errorf("output = %+v, want stop done, text done, 1 turn", out)
	}
	if te.stderr.Len() != 0 {
		t.Errorf("-quiet stderr = %q, want empty", te.stderr)
	}
}

func TestRunTaskIncomplete(t *testing.T) {
	te := newTestEnv(t, nil, llmtest.Reply{Response: llm.Response{StopReason: llm.StopRefusal}})

	if code := run(context.Background(), []string{"-p", "hi"}, te.env); code != exitIncomplete {
		t.Errorf("run = %d, want %d", code, exitIncomplete)
	}
	if !strings.Contains(te.stderr.String(), "stopped before finishing: refusal") {
		t.Errorf("stderr = %q, want the stop reason", te.stderr)
	}
}

func TestRunTaskTrace(t *testing.T) {
	te := newTestEnv(t, nil,
		llmtest.ToolUseReply(llm.ToolUse{ID: "t1", Name: "bash", Input: json.RawMessage(`{"command":"echo sk-secret"}`)}),
		llmtest.TextReply("done"),
	)

	if code := run(context.Background(), []string{"-p", "hi", "-trace", "-quiet"}, te.env); code != exitOK {
		t.Fatalf("run = %d, want %d; stderr: %s", code, exitOK, te.stderr)
	}
	files, err := filepath.Glob(filepath.Join(te.workdir, ".loom", "runs", "*.jsonl"))
	if err != nil || len(files) != 1 {
		t.Fatalf("trace files = %v, %v; want one", files, err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "sk-secret") {
		t.Error("trace contains the API key")
	}
	var names []string
	for line := range strings.Lines(string(data)) {
		var rec struct {
			Event string `json:"event"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("trace line is not JSON: %v\n%s", err, line)
		}
		names = append(names, rec.Event)
	}
	want := "SessionStart UserPromptSubmit ModelRequest ModelResponse PreToolUse PostToolUse PostToolBatch " +
		"ModelRequest ModelResponse Stop SessionEnd"
	if got := strings.Join(names, " "); got != want {
		t.Errorf("trace events =\n%s\nwant\n%s", got, want)
	}
}
