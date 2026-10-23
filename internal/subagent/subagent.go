// Package subagent provides the agent tool: it hands one task to a fresh agent
// and returns only that agent's final report, so the details of the work stay
// out of the caller's context.
package subagent

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/xzwache/loom/internal/agent"
	"github.com/xzwache/loom/internal/event"
	"github.com/xzwache/loom/internal/llm"
	"github.com/xzwache/loom/internal/tool"
)

const TypeGeneral = "general-purpose"

// Tool starts subagents of the types it was built with.
type Tool struct {
	template agent.Agent
	types    map[string]prepared
	spec     llm.ToolSpec
}

type prepared struct {
	Type
	tools *tool.Registry
}

// New prepares the agent tool. template holds what every subagent shares: the
// model client, limits and hooks; its Tools are every tool a subagent may get
// and must not include the agent tool, so subagents cannot start their own;
// its System is appended after each type's prompt.
func New(template agent.Agent, types []Type) (*Tool, error) {
	all := template.Tools
	if all == nil {
		all, _ = tool.NewRegistry()
	}
	types = slices.SortedFunc(slices.Values(types), func(a, b Type) int { return strings.Compare(a.Name, b.Name) })
	t := &Tool{template: template, types: map[string]prepared{}}
	var names, list []string
	for _, typ := range types {
		t.types[typ.Name] = prepared{Type: typ, tools: typeTools(all, typ, template.Log)}
		names = append(names, typ.Name)
		list = append(list, "- "+typ.Name+": "+typ.Description)
	}
	if _, ok := t.types[TypeGeneral]; !ok {
		return nil, fmt.Errorf("agent type %s is missing", TypeGeneral)
	}

	enum, _ := json.Marshal(names)
	t.spec = llm.ToolSpec{
		Name: tool.Agent,
		Description: "Start a subagent with a fresh context to do one self-contained task, so its details stay out of your " +
			"context. The subagent cannot see this conversation, so the prompt must say everything it needs, including " +
			"what to report back. Only its final report comes back to you.\n\nAgent types:\n" + strings.Join(list, "\n"),
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"description": {"type": "string", "description": "A short (3-5 word) description of the task"},
				"prompt": {"type": "string", "description": "The task for the subagent"},
				"subagent_type": {"type": "string", "enum": ` + string(enum) + `, "description": "The type of agent to use; default ` + TypeGeneral + `"}
			},
			"required": ["description", "prompt"]
		}`),
	}
	return t, nil
}

// typeTools limits a type to the subagent tools it lists. Tools the
// subagent may not use are left out, so subagent.tools stays the ceiling.
func typeTools(all *tool.Registry, typ Type, log *slog.Logger) *tool.Registry {
	if len(typ.Tools) == 0 {
		return all
	}
	tools, unmatched := all.Select(typ.Tools...)
	for _, name := range unmatched {
		if log != nil {
			log.Warn("agent type lists a tool subagents cannot use", "type", typ.Name, "tool", name, "path", typ.Path)
		}
	}
	return tools
}

func (t *Tool) Spec() llm.ToolSpec { return t.spec }

func (t *Tool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Description  string `json:"description"`
		Prompt       string `json:"prompt"`
		SubagentType string `json:"subagent_type"`
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
	typ, ok := t.types[cmp.Or(in.SubagentType, TypeGeneral)]
	if !ok {
		return "", fmt.Errorf("unknown subagent_type %q", in.SubagentType)
	}

	a := t.template
	a.Subagent = event.Agent{ID: newID(), Type: typ.Name}
	a.ModelID = cmp.Or(typ.Model, a.ModelID)
	a.Tools = typ.tools
	a.System = strings.TrimSpace(typ.Prompt + "\n\n" + a.System)
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
		a.Log.Info("subagent", "id", a.Subagent.ID, "type", typ.Name, "description", in.Description, "stop", res.Stop,
			"turns", res.Turns, "input_tokens", res.Usage.InputTokens, "output_tokens", res.Usage.OutputTokens)
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
