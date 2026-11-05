package app

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/xzwache/loom/internal/agent"
	"github.com/xzwache/loom/internal/config"
	"github.com/xzwache/loom/internal/llm"
	"github.com/xzwache/loom/internal/llm/llmtest"
)

func printf(id, text string) llm.ToolUse {
	in, _ := json.Marshal(map[string]string{"command": "printf " + text})
	return llm.ToolUse{ID: id, Name: "bash", Input: in}
}

func TestAllowAlwaysStopsAskingForLikeCommands(t *testing.T) {
	cfg := config.Default()
	cfg.Model = "test-model"
	cfg.Memory = config.Memory{}
	model := llmtest.NewReplay(
		llmtest.Reply{Response: llm.Response{Content: []llm.Block{printf("t1", "hi")}, StopReason: llm.StopToolUse}},
		llmtest.Reply{Response: llm.Response{Content: []llm.Block{printf("t2", "hi again")}, StopReason: llm.StopToolUse}},
		llmtest.TextReply("done"),
	)
	var asked []agent.Approval
	opts := Options{
		Workdir: t.TempDir(),
		Dirs:    Dirs{Home: t.TempDir(), User: t.TempDir(), State: t.TempDir()},
		Model:   model,
		Approve: func(_ context.Context, a agent.Approval) (agent.Answer, error) {
			asked = append(asked, a)
			return agent.AllowAlways, nil
		},
	}
	conv, err := Build(context.Background(), cfg, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer conv.Close()

	if _, err := conv.Run(context.Background(), "print twice"); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 {
		t.Fatalf("asked %d times, want once", len(asked))
	}
	if want := []string{"Bash(printf hi *)"}; !cmp.Equal(asked[0].Always, want) {
		t.Errorf("Always = %v, want %v", asked[0].Always, want)
	}
	results := []string{}
	for _, m := range conv.Session.History.Messages() {
		for _, b := range m.Content {
			if r, ok := b.(llm.ToolResult); ok {
				results = append(results, r.Content)
			}
		}
	}
	want := []string{"hi", "hi"} // printf ignores the extra word
	if !cmp.Equal(results, want) {
		t.Errorf("tool results = %q, want %q", results, want)
	}
}

func TestAllowAlwaysNotOffered(t *testing.T) {
	cfg := config.Default()
	cfg.Model = "test-model"
	cfg.Permissions.Ask = []string{"Bash(printf *)"}
	model := llmtest.NewReplay(
		llmtest.Reply{Response: llm.Response{Content: []llm.Block{printf("t1", "hi")}, StopReason: llm.StopToolUse}},
		llmtest.TextReply("done"),
	)
	opts := Options{
		Workdir: t.TempDir(),
		Dirs:    Dirs{Home: t.TempDir(), User: t.TempDir(), State: t.TempDir()},
		Model:   model,
		Approve: func(context.Context, agent.Approval) (agent.Answer, error) { return agent.AllowAlways, nil },
	}
	conv, err := Build(context.Background(), cfg, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer conv.Close()

	if _, err := conv.Run(context.Background(), "print"); err != nil {
		t.Fatal(err)
	}
	msgs := conv.Session.History.Messages()
	r := msgs[2].Content[0].(llm.ToolResult)
	if want := "denied: approval failed: allow always was not offered for this call"; r.Content != want {
		t.Errorf("result = %q, want %q", r.Content, want)
	}
}
