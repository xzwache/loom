//go:build integration

package agent

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xzwache/loom/internal/llm"
	"github.com/xzwache/loom/internal/tool"
)

func TestAgentCreatesFile(t *testing.T) {
	key, model := os.Getenv("ANTHROPIC_API_KEY"), os.Getenv("LOOM_MODEL")
	if key == "" || model == "" {
		t.Skip("ANTHROPIC_API_KEY and LOOM_MODEL must be set")
	}
	dir := t.TempDir()
	a := &Agent{
		Model:     llm.NewAnthropic(llm.AnthropicOptions{APIKey: key, Timeout: 2 * time.Minute, MaxRetries: 2}),
		ModelID:   model,
		System:    "You are a coding agent working in " + dir + ".",
		MaxTokens: 4096,
		Tools:     []tool.Tool{&tool.Bash{Dir: dir, Timeout: 30 * time.Second, MaxOutput: 10_000}},
		Budget:    Budget{MaxTurns: 6, MaxDuration: 3 * time.Minute},
		Log:       slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}

	res, err := a.Run(context.Background(), []llm.Message{{Role: llm.RoleUser, Content: []llm.Block{
		llm.Text{Text: "Create a file named hello.txt whose only content is the word hi. Then confirm it exists."},
	}}})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if res.Stop != StopDone {
		t.Fatalf("Run stop = %q after %d turns, want done", res.Stop, res.Turns)
	}
	data, err := os.ReadFile(filepath.Join(dir, "hello.txt"))
	if err != nil || strings.TrimSpace(string(data)) != "hi" {
		t.Errorf("hello.txt = %q, %v; want hi", data, err)
	}
	t.Logf("turns %d, usage %+v", res.Turns, res.Usage)
}
