package session

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/xzwache/loom/internal/llm"
)

// History is the message list of a session. It keeps it valid to send: every
// tool use is answered by a result in the next message.
type History struct {
	messages []llm.Message
}

func NewHistory(msgs []llm.Message) (History, error) {
	h := History{messages: slices.Clone(msgs)}
	if err := h.Validate(); err != nil {
		return History{}, err
	}
	return h, nil
}

func (h History) Messages() []llm.Message { return slices.Clone(h.messages) }
func (h History) Len() int                { return len(h.messages) }
func (h History) clone() History          { return History{messages: slices.Clone(h.messages)} }

func (h *History) Append(msgs ...llm.Message) { h.messages = append(h.messages, msgs...) }

// AddPrompt appends the user's text as the next user turn, with loom's
// notes after it. When the history ends with a user message (tool results
// from a run that stopped early), they join it, since the API rejects two
// user messages in a row.
func (h *History) AddPrompt(text string, notes ...llm.Text) {
	blocks := []llm.Block{llm.UserText(text)}
	for _, n := range notes {
		blocks = append(blocks, n)
	}
	if n := len(h.messages); n > 0 && h.messages[n-1].Role == llm.RoleUser {
		last := &h.messages[n-1]
		last.Content = append(slices.Clone(last.Content), blocks...)
		return
	}
	h.Append(llm.Message{Role: llm.RoleUser, Content: blocks})
}

func (h History) Validate() error {
	for i, m := range h.messages {
		if err := m.Check(); err != nil {
			return fmt.Errorf("message %d: %w", i, err)
		}
		if m.Role != llm.RoleAssistant {
			continue
		}
		answered := map[string]bool{}
		if i+1 < len(h.messages) && h.messages[i+1].Role == llm.RoleUser {
			for _, b := range h.messages[i+1].Content {
				if r, ok := b.(llm.ToolResult); ok {
					answered[r.ToolUseID] = true
				}
			}
		}
		for _, b := range m.Content {
			if u, ok := b.(llm.ToolUse); ok && !answered[u.ID] {
				return fmt.Errorf("message %d: tool use %s has no result", i, u.ID)
			}
		}
	}
	return nil
}

func (h History) MarshalJSON() ([]byte, error) {
	if h.messages == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(h.messages)
}

func (h *History) UnmarshalJSON(data []byte) error {
	var msgs []llm.Message
	if err := json.Unmarshal(data, &msgs); err != nil {
		return err
	}
	loaded, err := NewHistory(msgs)
	if err != nil {
		return err
	}
	*h = loaded
	return nil
}
