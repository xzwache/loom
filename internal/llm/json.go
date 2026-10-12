package llm

import (
	"encoding/json"
	"fmt"
)

// jsonBlock is the stored form of a Block, tagged with its type.
type jsonBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	Signature string          `json:"signature,omitempty"`
	Data      string          `json:"data,omitempty"`
}

type jsonMessage struct {
	Role    Role        `json:"role"`
	Content []jsonBlock `json:"content"`
}

func (m Message) MarshalJSON() ([]byte, error) {
	out := jsonMessage{Role: m.Role, Content: make([]jsonBlock, 0, len(m.Content))}
	for _, b := range m.Content {
		switch b := b.(type) {
		case Text:
			out.Content = append(out.Content, jsonBlock{Type: "text", Text: b.Text})
		case ToolUse:
			out.Content = append(out.Content, jsonBlock{Type: "tool_use", ID: b.ID, Name: b.Name, Input: b.Input})
		case ToolResult:
			out.Content = append(out.Content, jsonBlock{Type: "tool_result", ToolUseID: b.ToolUseID, Content: b.Content, IsError: b.IsError})
		case Thinking:
			out.Content = append(out.Content, jsonBlock{Type: "thinking", Thinking: b.Thinking, Signature: b.Signature})
		case RedactedThinking:
			out.Content = append(out.Content, jsonBlock{Type: "redacted_thinking", Data: b.Data})
		default:
			return nil, fmt.Errorf("%w: block type %T", ErrUnexpectedResponse, b)
		}
	}
	return json.Marshal(out)
}

func (m *Message) UnmarshalJSON(data []byte) error {
	var in jsonMessage
	if err := json.Unmarshal(data, &in); err != nil {
		return err
	}
	msg := Message{Role: in.Role}
	for _, b := range in.Content {
		switch b.Type {
		case "text":
			msg.Content = append(msg.Content, Text{Text: b.Text})
		case "tool_use":
			msg.Content = append(msg.Content, ToolUse{ID: b.ID, Name: b.Name, Input: b.Input})
		case "tool_result":
			msg.Content = append(msg.Content, ToolResult{ToolUseID: b.ToolUseID, Content: b.Content, IsError: b.IsError})
		case "thinking":
			msg.Content = append(msg.Content, Thinking{Thinking: b.Thinking, Signature: b.Signature})
		case "redacted_thinking":
			msg.Content = append(msg.Content, RedactedThinking{Data: b.Data})
		default:
			return fmt.Errorf("unknown block type %q", b.Type)
		}
	}
	*m = msg
	return nil
}
