package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
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
	model  *llmtest.Replay
}

func newTestEnv(t *testing.T, vars map[string]string, replies ...llmtest.Reply) *testEnv {
	t.Helper()
	if vars == nil {
		vars = map[string]string{"ANTHROPIC_API_KEY": "sk-secret", "LOOM_MODEL": "test-model"}
	}
	vars["HOME"] = t.TempDir()
	// Memory adds a tool and a model call at the end; memory_test.go covers it.
	writeFile(t, userConfig(vars["HOME"]), `{"memory": {"enabled": false}}`)
	te := &testEnv{stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, model: llmtest.NewReplay(replies...)}
	te.env = env{
		stdout:   te.stdout,
		stderr:   te.stderr,
		getenv:   func(k string) string { return vars[k] },
		workdir:  t.TempDir(),
		newModel: func(config.Config) llm.Model { return te.model },
	}
	return te
}

func userConfig(home string) string {
	return filepath.Join(home, ".config", "loom", "config.json")
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

func TestRunHeadless(t *testing.T) {
	te := newTestEnv(t, nil,
		llmtest.ToolUseReply(llm.ToolUse{ID: "t1", Name: "bash", Input: json.RawMessage(`{"command":"echo hi > out.txt"}`)}),
		llmtest.TextReply("Created out.txt."),
	)

	writeFile(t, filepath.Join(te.workdir, ".loom", "config.json"), `{"permissions": {"allow": ["Bash(echo *)"]}}`)

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
	if req.Model != "test-model" || !strings.Contains(req.System, te.workdir) || len(req.Tools) != 8 {
		t.Errorf("first request = model %q, system %q, %d tools", req.Model, req.System, len(req.Tools))
	}
}

func TestRunHeadlessJSON(t *testing.T) {
	te := newTestEnv(t, nil, llmtest.TextReply("done"))

	if code := run(context.Background(), []string{"-p", "hi", "-json", "-quiet"}, te.env); code != exitOK {
		t.Fatalf("run = %d, want %d; stderr: %s", code, exitOK, te.stderr)
	}
	var out headlessOutput
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

func TestRunHeadlessIncomplete(t *testing.T) {
	te := newTestEnv(t, nil, llmtest.Reply{Response: llm.Response{StopReason: llm.StopRefusal}})

	if code := run(context.Background(), []string{"-p", "hi"}, te.env); code != exitIncomplete {
		t.Errorf("run = %d, want %d", code, exitIncomplete)
	}
	if !strings.Contains(te.stderr.String(), "stopped before finishing: refusal") {
		t.Errorf("stderr = %q, want the stop reason", te.stderr)
	}
}

func TestRunHeadlessTrace(t *testing.T) {
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

func TestRunHeadlessPermissions(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "no rule asks, and -p denies", want: "denied: needs approval"},
		{name: "plan mode", args: []string{"-permission-mode", "plan"}, want: "denied: plan mode allows only reading"},
		{name: "floor", args: []string{"-permission-mode", "bypassPermissions"}, want: "denied: never allowed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			command := "touch x"
			if tt.name == "floor" {
				command = "sudo rm -rf /"
			}
			te := newTestEnv(t, nil,
				llmtest.ToolUseReply(llm.ToolUse{ID: "t1", Name: "bash", Input: json.RawMessage(`{"command":"` + command + `"}`)}),
				llmtest.TextReply("ok"),
			)

			if code := run(context.Background(), append([]string{"-p", "hi", "-quiet"}, tt.args...), te.env); code != exitOK {
				t.Fatalf("run = %d, want %d; stderr: %s", code, exitOK, te.stderr)
			}
			msgs := te.model.Requests()[1].Messages
			result := msgs[len(msgs)-1].Content[0].(llm.ToolResult)
			if !result.IsError || !strings.HasPrefix(result.Content, tt.want) {
				t.Errorf("tool result = %+v, want an error starting with %q", result, tt.want)
			}
		})
	}
}

func TestRunInvalidPermissionMode(t *testing.T) {
	te := newTestEnv(t, nil)
	if code := run(context.Background(), []string{"-p", "hi", "-permission-mode", "yolo"}, te.env); code != exitError {
		t.Errorf("run = %d, want %d", code, exitError)
	}
	if !strings.Contains(te.stderr.String(), `unknown permission mode "yolo"`) {
		t.Errorf("stderr = %q, want the bad mode", te.stderr)
	}
}

func writeFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRunHeadlessCommandHook(t *testing.T) {
	te := newTestEnv(t, nil,
		llmtest.ToolUseReply(llm.ToolUse{ID: "t1", Name: "bash", Input: json.RawMessage(`{"command":"ls"}`)}),
		llmtest.TextReply("ok"),
	)
	writeFile(t, filepath.Join(te.workdir, ".loom", "config.json"), `{"hooks": {"PreToolUse": [
		{"matcher": "Bash", "hooks": [{"type": "command", "command": "grep -q '\"tool_name\":\"Bash\"' && echo 'no shell today' >&2 && exit 2"}]}
	]}}`)

	if code := run(context.Background(), []string{"-p", "hi", "-quiet"}, te.env); code != exitOK {
		t.Fatalf("run = %d, want %d; stderr: %s", code, exitOK, te.stderr)
	}
	msgs := te.model.Requests()[1].Messages
	result := msgs[len(msgs)-1].Content[0].(llm.ToolResult)
	if !result.IsError || result.Content != "denied: no shell today" {
		t.Errorf("tool result = %+v, want the hook's denial", result)
	}
}

func TestRunHeadlessContinue(t *testing.T) {
	te := newTestEnv(t, nil, llmtest.TextReply("first answer"), llmtest.TextReply("second answer"))

	if code := run(context.Background(), []string{"-p", "first", "-json", "-quiet"}, te.env); code != exitOK {
		t.Fatalf("first run = %d; stderr: %s", code, te.stderr)
	}
	var out headlessOutput
	if err := json.Unmarshal(te.stdout.Bytes(), &out); err != nil || out.SessionID == "" {
		t.Fatalf("first output = %s, %v; want a session id", te.stdout, err)
	}

	if code := run(context.Background(), []string{"-p", "second", "-continue", "-quiet"}, te.env); code != exitOK {
		t.Fatalf("second run = %d; stderr: %s", code, te.stderr)
	}
	sent := te.model.Requests()[1].Messages
	var texts []string
	for _, m := range sent {
		texts = append(texts, string(m.Role)+":"+m.Content[0].(llm.Text).Text)
	}
	if want := "user:first assistant:first answer user:second"; strings.Join(texts, " ") != want {
		t.Errorf("second request = %v, want %s", texts, want)
	}

	if code := run(context.Background(), []string{"-p", "x", "-resume", "nope"}, te.env); code != exitError {
		t.Errorf("resume of a missing session = %d, want %d", code, exitError)
	}
}

func TestRunHeadlessTodoAndInstructions(t *testing.T) {
	te := newTestEnv(t, nil,
		llmtest.ToolUseReply(llm.ToolUse{ID: "t1", Name: "todo", Input: json.RawMessage(`{"todos":[{"content":"fix","status":"in_progress"}]}`)}),
		llmtest.TextReply("done"),
	)
	writeFile(t, filepath.Join(te.workdir, "AGENTS.md"), "Always run make check.")

	if code := run(context.Background(), []string{"-p", "hi", "-quiet"}, te.env); code != exitOK {
		t.Fatalf("run = %d; stderr: %s", code, te.stderr)
	}
	if sys := te.model.Requests()[0].System; !strings.Contains(sys, "Always run make check.") {
		t.Errorf("system prompt lacks AGENTS.md: %q", sys)
	}
	msgs := te.model.Requests()[1].Messages
	if result := msgs[len(msgs)-1].Content[0].(llm.ToolResult); result.IsError || result.Content != "[>] fix\n" {
		t.Errorf("todo result = %+v, want the rendered list", result)
	}
}

func TestRunHeadlessSubagent(t *testing.T) {
	te := newTestEnv(t, nil,
		llmtest.ToolUseReply(llm.ToolUse{ID: "t1", Name: "agent", Input: json.RawMessage(`{"description":"find main","prompt":"where is main?"}`)}),
		llmtest.TextReply("main is in main.go"),
		llmtest.TextReply("Done."),
	)
	writeFile(t, filepath.Join(te.workdir, ".loom", "config.json"), `{"subagent": {"model": "cheap-model", "tools": ["read", "glob"]}}`)

	if code := run(context.Background(), []string{"-p", "find main", "-quiet"}, te.env); code != exitOK {
		t.Fatalf("run = %d; stderr: %s", code, te.stderr)
	}
	reqs := te.model.Requests()
	sub := reqs[1]
	if sub.Model != "cheap-model" || !strings.Contains(sub.System, "loom subagent") || len(sub.Tools) != 2 || len(sub.Messages) != 1 {
		t.Errorf("subagent request = model %q, %d tools, %d messages, system %q", sub.Model, len(sub.Tools), len(sub.Messages), sub.System)
	}
	msgs := reqs[2].Messages
	if result := msgs[len(msgs)-1].Content[0].(llm.ToolResult); result.IsError || result.Content != "main is in main.go" {
		t.Errorf("agent result = %+v, want the subagent's report", result)
	}
}

func TestRunHeadlessAgentTypes(t *testing.T) {
	te := newTestEnv(t, nil,
		llmtest.ToolUseReply(llm.ToolUse{ID: "t1", Name: "agent", Input: json.RawMessage(`{"description":"review","prompt":"review it","subagent_type":"reviewer"}`)}),
		llmtest.TextReply("looks fine"),
		llmtest.TextReply("Done."),
	)
	writeFile(t, filepath.Join(te.workdir, ".loom", "agents", "reviewer.md"),
		"---\nname: reviewer\ndescription: Reviews changes.\ntools: Read, Glob\nmodel: review-model\n---\nYou review code.\n")

	if code := run(context.Background(), []string{"-p", "review", "-quiet"}, te.env); code != exitOK {
		t.Fatalf("run = %d; stderr: %s", code, te.stderr)
	}
	reqs := te.model.Requests()
	if spec := reqs[0].Tools[0]; spec.Name != "agent" || !strings.Contains(spec.Description, "- reviewer: Reviews changes.") {
		t.Errorf("agent tool = %+v, want the reviewer type listed", spec)
	}
	if sub := reqs[1]; sub.Model != "review-model" || !strings.HasPrefix(sub.System, "You review code.") || len(sub.Tools) != 2 {
		t.Errorf("subagent request = model %q, %d tools, system %q", sub.Model, len(sub.Tools), sub.System)
	}
}

func TestRunHeadlessSkills(t *testing.T) {
	te := newTestEnv(t, nil,
		llmtest.ToolUseReply(llm.ToolUse{ID: "t1", Name: "skill", Input: json.RawMessage(`{"skill":"release"}`)}),
		llmtest.TextReply("Released."),
	)
	writeFile(t, filepath.Join(te.workdir, ".loom", "skills", "release", "SKILL.md"),
		"---\nname: release\ndescription: Cut a release.\n---\nTag, then push.\n")

	if code := run(context.Background(), []string{"-p", "release it", "-trace", "-quiet"}, te.env); code != exitOK {
		t.Fatalf("run = %d; stderr: %s", code, te.stderr)
	}
	reqs := te.model.Requests()
	if !slices.ContainsFunc(reqs[0].Tools, func(s llm.ToolSpec) bool {
		return s.Name == "skill" && strings.Contains(s.Description, "- release: Cut a release.")
	}) {
		t.Error("the skill tool does not list the release skill")
	}
	msgs := reqs[1].Messages
	if result := msgs[len(msgs)-1].Content[0].(llm.ToolResult); result.IsError || !strings.HasSuffix(result.Content, "Tag, then push.") {
		t.Errorf("skill result = %+v, want the instructions", result)
	}

	files, _ := filepath.Glob(filepath.Join(te.workdir, ".loom", "runs", "*.jsonl"))
	if len(files) != 1 {
		t.Fatalf("trace files = %v", files)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"event":"SkillLoaded"`, `"event":"SkillUsed"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("trace lacks %s", want)
		}
	}
}

func TestRunHeadlessCompaction(t *testing.T) {
	full := llmtest.ToolUseReply(llm.ToolUse{ID: "t1", Name: "glob", Input: json.RawMessage(`{"pattern":"*"}`)})
	full.Response.Usage = llm.Usage{InputTokens: 900}
	te := newTestEnv(t, nil, full, llmtest.TextReply("Task: list files."), llmtest.TextReply("Done."))
	writeFile(t, filepath.Join(te.workdir, ".loom", "config.json"),
		`{"compaction": {"context_window": 1000, "threshold": 0.8, "target": 0.5, "keep_turns": 1}}`)

	if code := run(context.Background(), []string{"-p", "list files", "-trace", "-quiet"}, te.env); code != exitOK {
		t.Fatalf("run = %d; stderr: %s", code, te.stderr)
	}
	reqs := te.model.Requests()
	if len(reqs) != 3 || len(reqs[2].Messages) != 1 || !strings.Contains(reqs[2].Messages[0].Content[0].(llm.Text).Text, "Task: list files.") {
		t.Fatalf("got %d requests; the last should hold only the summary", len(reqs))
	}

	files, _ := filepath.Glob(filepath.Join(te.workdir, ".loom", "runs", "*.jsonl"))
	if len(files) != 1 {
		t.Fatalf("trace files = %v", files)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"event":"PreCompact"`, `"event":"PostCompact"`, `"strategy":"summarize"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("trace lacks %s", want)
		}
	}
}
