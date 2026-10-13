// Package subagent provides the agent tool: it hands one task to a fresh agent
// and returns only that agent's final report, so the details of the work stay
// out of the caller's context.
package subagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/xzwache/loom/internal/agent"
	"github.com/xzwache/loom/internal/event"
	"github.com/xzwache/loom/internal/llm"
)

const TypeGeneral = "general-purpose"

// Tool runs each subagent as a copy of Template with a fresh history.
// Template.Tools must not contain the agent tool, so subagents cannot start
// subagents of their own.
type Tool struct {
	Template agent.Agent
}

func (t *Tool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: "agent",
		Description: "Start a subagent with a fresh context to do one self-contained task, such as searching the codebase, " +
			"investigating a question or making a well-defined change. The subagent cannot see this conversation, so the " +
			"prompt must say everything it needs, including what to report back. Only its final report comes back to you.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"description": {"type": "string", "description": "A short (3-5 word) description of the task"},
				"prompt": {"type": "string", "description": "The task for the subagent"}
			},
			"required": ["description", "prompt"]
		}`),
	}
}

func (t *Tool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Description string `json:"description"`
		Prompt      string `json:"prompt"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if strings.TrimSpace(in.Prompt) == "" {
		return "", errors.New("prompt is empty")
	}
	if _, nested := event.AgentFrom(ctx); nested {
		return "", errors.New("a subagent cannot start subagents")
	}

	a := t.Template
	a.Subagent = event.Agent{ID: newID(), Type: TypeGeneral}
	prompt := in.Prompt
	start, err := a.Hooks.Fire(ctx, event.SubagentStart{AgentID: a.Subagent.ID, AgentType: a.Subagent.Type})
	if err != nil && a.Log != nil {
		a.Log.Warn("hook failed", "event", "SubagentStart", "error", err)
	}
	if start.AdditionalContext != "" {
		prompt += "\n\n" + start.AdditionalContext
	}

	res, err := a.Run(ctx, []llm.Message{{Role: llm.RoleUser, Content: []llm.Block{llm.Text{Text: prompt}}}})
	if err != nil {
		return "", fmt.Errorf("subagent %s: %w", a.Subagent.ID, err)
	}
	if a.Log != nil {
		a.Log.Info("subagent", "id", a.Subagent.ID, "description", in.Description, "stop", res.Stop, "turns", res.Turns,
			"input_tokens", res.Usage.InputTokens, "output_tokens", res.Usage.OutputTokens)
	}
	return report(res), nil
}

func report(res agent.Result) string {
	if res.Stop == agent.StopDone {
		if res.Text == "" {
			return "(the subagent finished without a report)"
		}
		return res.Text
	}
	why := string(res.Stop)
	if res.Detail != "" {
		why += ": " + res.Detail
	}
	out := fmt.Sprintf("The subagent stopped before finishing (%s) after %d turns.", why, res.Turns)
	if last := lastText(res.Messages); last != "" {
		out += " Its last message:\n\n" + last
	}
	return out
}

func lastText(messages []llm.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != llm.RoleAssistant {
			continue
		}
		for _, b := range messages[i].Content {
			if t, ok := b.(llm.Text); ok && t.Text != "" {
				return t.Text
			}
		}
	}
	return ""
}

func newID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "agent-" + hex.EncodeToString(b)
}
