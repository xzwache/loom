package compact

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"strings"

	"github.com/xzwache/loom/internal/llm"
	"github.com/xzwache/loom/internal/tool"
)

//go:embed summary.md
var summaryPrompt string

// transcriptOutputLimit caps each tool result in the transcript, so one huge
// output cannot fill the summary request.
const transcriptOutputLimit = 2000

// Summarize replaces the history with a summary the model writes. It is the
// last resort: it costs a model call and loses detail.
type Summarize struct {
	Model     llm.Model
	ModelID   string
	MaxTokens int
	// MaxInput caps the transcript, in tokens, so the summary request itself
	// fits the context window. Over it, the middle is cut: the start holds
	// the task, the end the latest state. 0 means no cap.
	MaxInput int
}

func (Summarize) Name() string { return "summarize" }

// Compact sends the history as a plain transcript, not as messages: the
// request then needs no tool definitions, and huge outputs can be cut.
func (s Summarize) Compact(ctx context.Context, in Input) (Result, error) {
	history := transcript(in.Messages)
	if s.MaxInput > 0 {
		history = tool.Truncate(history, s.MaxInput*charsPerToken)
	}
	ask := "Summarize this session:\n\n" + history
	if in.Instructions != "" {
		ask += "\n\nWhen summarizing, also follow these instructions:\n" + in.Instructions
	}
	resp, err := s.Model.SendMessage(ctx, llm.Request{
		Model:     s.ModelID,
		System:    summaryPrompt,
		Messages:  []llm.Message{userText(ask)},
		MaxTokens: s.MaxTokens,
	})
	if err != nil {
		return Result{}, err
	}
	summary := strings.TrimSpace(text(resp.Content))
	if summary == "" {
		return Result{}, errors.New("the model returned an empty summary")
	}
	note := llm.SystemText(llm.KindSummary, continuation(summary))
	return Result{Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.Block{note}}}, Usage: resp.Usage}, nil
}

func continuation(summary string) string {
	return "This session continues an earlier conversation that was compacted to save context. " +
		"Summary of it:\n\n" + summary + "\n\nContinue the task from where it left off."
}

func transcript(messages []llm.Message) string {
	var sb strings.Builder
	for _, m := range messages {
		for _, b := range m.Content {
			line := transcriptLine(m.Role, b)
			if line != "" {
				sb.WriteString(line + "\n\n")
			}
		}
	}
	return sb.String()
}

func transcriptLine(role llm.Role, b llm.Block) string {
	switch b := b.(type) {
	case llm.Text:
		if b.Origin == llm.OriginSystem {
			return fmt.Sprintf("[loom note: %s] %s", b.Kind, b.Text)
		}
		return fmt.Sprintf("[%s] %s", role, b.Text)
	case llm.ToolUse:
		return fmt.Sprintf("[tool call %s] %s", b.Name, b.Input)
	case llm.ToolResult:
		label := "tool result"
		if b.IsError {
			label = "tool error"
		}
		return fmt.Sprintf("[%s] %s", label, tool.Truncate(b.Content, transcriptOutputLimit))
	default:
		return ""
	}
}

func userText(s string) llm.Message {
	return llm.Message{Role: llm.RoleUser, Content: []llm.Block{llm.UserText(s)}}
}

func text(content []llm.Block) string {
	var parts []string
	for _, b := range content {
		if t, ok := b.(llm.Text); ok {
			parts = append(parts, t.Text)
		}
	}
	return strings.Join(parts, "\n")
}
