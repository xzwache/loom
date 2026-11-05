package compact

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xzwache/loom/internal/event"
	"github.com/xzwache/loom/internal/llm"
	"github.com/xzwache/loom/internal/llm/llmtest"
)

func user(blocks ...llm.Block) llm.Message {
	return llm.Message{Role: llm.RoleUser, Content: blocks}
}

func assistant(blocks ...llm.Block) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, Content: blocks}
}

// session has three turns, each a tool call and a long result.
func session() []llm.Message {
	long := strings.Repeat("x", 400)
	msgs := []llm.Message{user(llm.Text{Text: "fix the bug"})}
	for _, id := range []string{"t1", "t2", "t3"} {
		msgs = append(msgs,
			assistant(llm.ToolUse{ID: id, Name: "read", Input: json.RawMessage(`{}`)}),
			user(llm.ToolResult{ToolUseID: id, Content: long}),
		)
	}
	return msgs
}

func results(msgs []llm.Message) []string {
	var out []string
	for _, m := range msgs {
		for _, b := range m.Content {
			if r, ok := b.(llm.ToolResult); ok {
				out = append(out, r.Content)
			}
		}
	}
	return out
}

func archive(t *testing.T) Archive {
	return Archive{Root: t.TempDir(), Dir: filepath.Join(".loom", "outputs"), Session: "s1"}
}

func offloader(t *testing.T) OffloadOutputs {
	return OffloadOutputs{KeepTurns: 1, Archive: archive(t)}
}

func TestOffloadKeepsRecentTurns(t *testing.T) {
	in := session()
	r, err := offloader(t).Compact(context.Background(), Input{Messages: in})
	if err != nil {
		t.Fatal(err)
	}
	got := results(r.Messages)
	if !isOffloaded(got[0]) || !isOffloaded(got[1]) || isOffloaded(got[2]) {
		t.Errorf("results = %.40q, want the first two offloaded and the last kept", got)
	}
	if len(r.Messages) != len(in) {
		t.Errorf("%d messages, want %d: offloading must keep every message", len(r.Messages), len(in))
	}
	if isOffloaded(results(in)[0]) {
		t.Error("the input history was changed")
	}
}

func isOffloaded(s string) bool { return strings.HasPrefix(s, offloadedPrefix) }

func TestOffloadSavesOutputAndShowsThePath(t *testing.T) {
	o := offloader(t)
	in := session()
	r, err := o.Compact(context.Background(), Input{Messages: in})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(".loom", "outputs", "s1", "t1.txt")
	if marker := results(r.Messages)[0]; !strings.Contains(marker, path) || !strings.Contains(marker, "1 lines, 400 bytes") {
		t.Errorf("marker = %q, want the path %s and the size", marker, path)
	}
	data, err := os.ReadFile(filepath.Join(o.Archive.Root, path))
	if err != nil || string(data) != results(in)[0] {
		t.Errorf("saved output = %d bytes, %v; want the full result", len(data), err)
	}
	ignore, err := os.ReadFile(filepath.Join(o.Archive.Root, ".loom", "outputs", ".gitignore"))
	if err != nil || string(ignore) != "*\n" {
		t.Errorf(".gitignore = %q, %v; want everything ignored", ignore, err)
	}

	// A second pass must keep the saved output, not overwrite it with the marker.
	if _, err := o.Compact(context.Background(), Input{Messages: r.Messages}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(o.Archive.Root, path)); string(data) != results(in)[0] {
		t.Error("a second pass overwrote the saved output")
	}
}

func TestOffloadKeepsOutputItCannotSave(t *testing.T) {
	o := offloader(t)
	o.Archive.Root = filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(o.Archive.Root, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	in := session()
	r, err := o.Compact(context.Background(), Input{Messages: in})
	if err != nil {
		t.Fatal(err)
	}
	if r.Failed != 2 || r.FailErr == nil {
		t.Errorf("failed = %d, %v; want 2 failures with a reason", r.Failed, r.FailErr)
	}
	if got := results(r.Messages); got[0] != results(in)[0] {
		t.Error("an output that was not saved was removed from the history")
	}
}

func TestOffloadSkipsSmallGains(t *testing.T) {
	o := offloader(t)
	o.MinTokens = 1000
	in := session()
	r, err := o.Compact(context.Background(), Input{Messages: in})
	if err != nil {
		t.Fatal(err)
	}
	if isOffloaded(results(r.Messages)[0]) {
		t.Error("offloaded although it frees less than MinTokens")
	}
}

func TestOffloadKeepsShortResults(t *testing.T) {
	in := []llm.Message{
		assistant(llm.ToolUse{ID: "t1", Name: "bash"}),
		user(llm.ToolResult{ToolUseID: "t1", Content: "ok"}),
		assistant(llm.Text{Text: "done"}),
	}
	r, _ := OffloadOutputs{KeepTurns: 1, Archive: archive(t)}.Compact(context.Background(), Input{Messages: in})
	if got := results(r.Messages)[0]; got != "ok" {
		t.Errorf("result = %q, want a short result kept", got)
	}
}

func TestArchiveRedactsAndSeparatesSubagents(t *testing.T) {
	a := archive(t)
	a.Redact = func(s string) string { return strings.ReplaceAll(s, "sk-secret", "[REDACTED]") }
	ctx := event.WithAgent(context.Background(), event.Agent{ID: "agent-1"})
	if err := a.Save(ctx, "t1", "key=sk-secret"); err != nil {
		t.Fatal(err)
	}
	path := a.Path(ctx, "t1")
	if path != filepath.Join(".loom", "outputs", "s1", "agent-1", "t1.txt") {
		t.Errorf("subagent path = %s, want its own folder", path)
	}
	if data, _ := os.ReadFile(filepath.Join(a.Root, path)); string(data) != "key=[REDACTED]" {
		t.Errorf("saved %q, want the secret redacted", data)
	}
}

func TestArchivePathIsSafe(t *testing.T) {
	a := Archive{Dir: "out", Session: "s1"}
	if got := a.Path(context.Background(), "../../etc/passwd"); got != filepath.Join("out", "s1", "______etc_passwd.txt") {
		t.Errorf("Path = %q, want a name inside the folder", got)
	}
}

func TestArchivePrunesOldSessions(t *testing.T) {
	a := archive(t)
	for _, s := range []string{"old", "new", "s1"} {
		b := a
		b.Session = s
		if err := b.Save(context.Background(), "t1", "x"); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(a.Root, a.Dir)
	week := 7 * 24 * time.Hour
	past := time.Now().Add(-2 * week)
	for _, s := range []string{"old", "s1"} {
		if err := os.Chtimes(filepath.Join(dir, s), past, past); err != nil {
			t.Fatal(err)
		}
	}

	if err := a.Prune(week, time.Now()); err != nil {
		t.Fatal(err)
	}
	for s, want := range map[string]bool{"old": false, "new": true, "s1": true} {
		if _, err := os.Stat(filepath.Join(dir, s)); (err == nil) != want {
			t.Errorf("%s exists = %v, want %v", s, err == nil, want)
		}
	}
}

type fixed struct {
	out   []llm.Message
	usage llm.Usage
	err   error
	ran   *bool
}

func (fixed) Name() string { return "fixed" }

func (f fixed) Compact(context.Context, Input) (Result, error) {
	if f.ran != nil {
		*f.ran = true
	}
	return Result{Messages: f.out, Usage: f.usage}, f.err
}

func TestChainStopsWhenSmallEnough(t *testing.T) {
	ranSummary := false
	c := Chain{
		Strategies: []Strategy{offloader(t), fixed{ran: &ranSummary}},
		Trigger:    1000,
		Target:     900,
	}
	out, reports, err := c.Run(context.Background(), Input{Messages: session()}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if ranSummary || len(reports) != 1 || reports[0].Strategy != "offload_outputs" {
		t.Errorf("reports = %+v, summary ran = %v; want clearing only", reports, ranSummary)
	}
	if r := reports[0]; r.TokensBefore != 1000 || r.TokensAfter >= 1000 {
		t.Errorf("report = %+v, want fewer tokens after", r)
	}
	if !isOffloaded(results(out)[0]) {
		t.Error("the history was not offloaded")
	}
}

func TestChainSummarizesWhenClearingIsNotEnough(t *testing.T) {
	summary := []llm.Message{user(llm.Text{Text: "summary"})}
	c := Chain{
		Strategies: []Strategy{offloader(t), fixed{out: summary, usage: llm.Usage{InputTokens: 7}}},
		Trigger:    1000,
		Target:     100,
	}
	out, reports, err := c.Run(context.Background(), Input{Messages: session()}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 2 || reports[1].Usage.InputTokens != 7 || len(out) != 1 {
		t.Errorf("reports = %+v, %d messages; want both strategies and the summary", reports, len(out))
	}
}

func TestChainKeepsEarlierWorkWhenAStrategyFails(t *testing.T) {
	c := Chain{
		Strategies: []Strategy{offloader(t), fixed{err: errors.New("api down")}},
		Trigger:    1000,
		Target:     10,
	}
	out, reports, err := c.Run(context.Background(), Input{Messages: session()}, 1000)
	if err == nil || len(reports) != 1 || !isOffloaded(results(out)[0]) {
		t.Errorf("err = %v, reports = %+v; want the error and the cleared history", err, reports)
	}
}

func TestChainNeeded(t *testing.T) {
	c := Chain{Trigger: 100}
	if c.Needed(99) || !c.Needed(100) || (Chain{}).Needed(1_000_000) {
		t.Error("Needed is wrong")
	}
}

func TestSummarize(t *testing.T) {
	model := llmtest.NewReplay(llmtest.TextReply("Task: fix the bug."))
	s := Summarize{Model: model, ModelID: "cheap", MaxTokens: 100}
	r, err := s.Compact(context.Background(), Input{Messages: session(), Instructions: "keep failing tests"})
	if err != nil {
		t.Fatal(err)
	}
	req := model.Requests()[0]
	ask := req.Messages[0].Content[0].(llm.Text).Text
	if req.Model != "cheap" || len(req.Tools) != 0 || !strings.Contains(ask, "[tool call read]") || !strings.Contains(ask, "keep failing tests") {
		t.Errorf("request = model %q, %d tools, ask %.200q", req.Model, len(req.Tools), ask)
	}
	if len(r.Messages) != 1 || !strings.Contains(r.Messages[0].Content[0].(llm.Text).Text, "Task: fix the bug.") {
		t.Errorf("history = %+v, want one message with the summary", r.Messages)
	}
}

func TestSummarizeCapsTheTranscript(t *testing.T) {
	model := llmtest.NewReplay(llmtest.TextReply("Task: fix the bug."))
	s := Summarize{Model: model, MaxInput: 100} // about 400 bytes
	if _, err := s.Compact(context.Background(), Input{Messages: session()}); err != nil {
		t.Fatal(err)
	}
	ask := model.Requests()[0].Messages[0].Content[0].(llm.Text).Text
	if len(ask) > 600 || !strings.Contains(ask, "fix the bug") || !strings.Contains(ask, "truncated") {
		t.Errorf("ask = %d bytes, want under 600 with the task kept and the middle cut:\n%s", len(ask), ask)
	}
}

func TestSummarizeRejectsEmptySummary(t *testing.T) {
	s := Summarize{Model: llmtest.NewReplay(llmtest.TextReply("  "))}
	if _, err := s.Compact(context.Background(), Input{Messages: session()}); err == nil {
		t.Error("an empty summary was accepted")
	}
}
