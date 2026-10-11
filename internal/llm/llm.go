// Package llm defines loom's provider-neutral conversation types and the Model
// interface the agent loop talks to. Provider SDK types never leave this package.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

type Model interface {
	SendMessage(ctx context.Context, req Request) (Response, error)
}

type Request struct {
	Model     string
	System    string
	Messages  []Message
	Tools     []ToolSpec
	MaxTokens int
}

type Response struct {
	Content    []Block
	StopReason StopReason
	Usage      Usage
}

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

type Message struct {
	Role    Role
	Content []Block
}

// Block is one piece of message content. The set of block types is closed:
// handle them with a type switch.
type Block interface{ isBlock() }

type Text struct {
	Text string
}

type ToolUse struct {
	ID    string
	Name  string
	Input json.RawMessage
}

type ToolResult struct {
	ToolUseID string
	Content   string
	IsError   bool
}

// Thinking and RedactedThinking must be sent back exactly as received, in
// their original position, for the rest of the conversation.
type Thinking struct {
	Thinking  string
	Signature string
}

type RedactedThinking struct {
	Data string
}

func (Text) isBlock()             {}
func (ToolUse) isBlock()          {}
func (ToolResult) isBlock()       {}
func (Thinking) isBlock()         {}
func (RedactedThinking) isBlock() {}

type ToolSpec struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

type StopReason string

const (
	StopEndTurn               StopReason = "end_turn"
	StopToolUse               StopReason = "tool_use"
	StopMaxTokens             StopReason = "max_tokens"
	StopSequence              StopReason = "stop_sequence"
	StopPauseTurn             StopReason = "pause_turn"
	StopRefusal               StopReason = "refusal"
	StopContextWindowExceeded StopReason = "model_context_window_exceeded"
)

type Usage struct {
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
}

func (u Usage) Add(o Usage) Usage {
	return Usage{
		InputTokens:      u.InputTokens + o.InputTokens,
		OutputTokens:     u.OutputTokens + o.OutputTokens,
		CacheReadTokens:  u.CacheReadTokens + o.CacheReadTokens,
		CacheWriteTokens: u.CacheWriteTokens + o.CacheWriteTokens,
	}
}

// ErrUnexpectedResponse means the provider returned something loom does not
// understand, such as an unknown stop reason or content block type.
var ErrUnexpectedResponse = errors.New("unexpected model response")

// APIError is a non-2xx response from the provider, after retries.
type APIError struct {
	StatusCode int
	Type       string
	RequestID  string
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("model API error %d %s (request %s): %s", e.StatusCode, e.Type, e.RequestID, e.Message)
}
