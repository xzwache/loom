package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/xzwache/loom/internal/llm"
	"github.com/xzwache/loom/internal/llm/llmtest"
)

// subTool runs a child agent, as the agent tool does.
func subTool(t *testing.T) funcTool {
	return funcTool{name: "sub", run: func(ctx context.Context, _ json.RawMessage) (string, error) {
		child := newAgent(t, llmtest.NewScripted(reply(llm.StopEndTurn, llm.Text{Text: "report"})))
		res, err := child.Run(ctx, prompt())
		return res.Text, err
	}}
}

func TestRunCountsSubagentUsage(t *testing.T) {
	model := llmtest.NewScripted(
		reply(llm.StopToolUse, use("t1", "sub")),
		reply(llm.StopEndTurn, llm.Text{Text: "done"}),
	)
	res, err := newAgent(t, model, subTool(t)).Run(context.Background(), prompt())
	if err != nil {
		t.Fatal(err)
	}
	if want := (llm.Usage{InputTokens: 30, OutputTokens: 3}); res.Usage != want {
		t.Errorf("usage = %+v, want %+v (two parent turns and one subagent turn)", res.Usage, want)
	}
}

func TestRunSubagentUsageCountsInBudget(t *testing.T) {
	model := llmtest.NewScripted(
		reply(llm.StopToolUse, use("t1", "sub")),
		reply(llm.StopEndTurn, llm.Text{Text: "done"}),
	)
	a := newAgent(t, model, subTool(t))
	a.Budget.MaxTokens = 20
	res, err := a.Run(context.Background(), prompt())
	if err != nil {
		t.Fatal(err)
	}
	if res.Stop != StopMaxRunTokens || res.Turns != 1 {
		t.Errorf("stop = %s after %d turns, want %s after 1", res.Stop, res.Turns, StopMaxRunTokens)
	}
}

func TestRunAfterCountsSpentTokens(t *testing.T) {
	model := llmtest.NewScripted(reply(llm.StopToolUse, use("n", "echo")), reply(llm.StopEndTurn, llm.Text{Text: "unreached"}))
	a := newAgent(t, model, echoTool())
	a.Budget.MaxTokens = 20
	// 15 spent before the run, then the first turn's 11 go over 20.
	res, err := a.RunAfter(context.Background(), prompt(), llm.Usage{InputTokens: 15})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stop != StopMaxRunTokens || res.Turns != 1 || res.Usage.InputTokens < 15 {
		t.Errorf("RunAfter = %s after %d turns, usage %+v; want max_run_tokens after 1 with the spent tokens", res.Stop, res.Turns, res.Usage)
	}
}
