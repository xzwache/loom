package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"
)

type fakeAPI struct {
	t        *testing.T
	statuses []int // returned in order before the final 200
	body     string
	calls    atomic.Int32
	lastBody []byte
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n := int(f.calls.Add(1))
	b, err := io.ReadAll(r.Body)
	if err != nil {
		f.t.Errorf("read request body: %v", err)
	}
	f.lastBody = b
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Request-Id", "req_test")
	if n <= len(f.statuses) {
		w.WriteHeader(f.statuses[n-1])
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"overloaded_error","message":"try later"}}`)
		return
	}
	_, _ = io.WriteString(w, f.body)
}

func newTestModel(t *testing.T, api *fakeAPI, maxRetries int) *Anthropic {
	t.Helper()
	api.t = t
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	return NewAnthropic(AnthropicOptions{APIKey: "test-key", BaseURL: srv.URL, MaxRetries: maxRetries})
}

const toolUseResponse = `{
	"id": "msg_1", "type": "message", "role": "assistant", "model": "test-model",
	"content": [
		{"type": "thinking", "thinking": "check files", "signature": "sig_1"},
		{"type": "redacted_thinking", "data": "opaque"},
		{"type": "text", "text": "Listing files."},
		{"type": "tool_use", "id": "tu_1", "name": "bash", "input": {"command": "ls"}}
	],
	"stop_reason": "tool_use",
	"usage": {"input_tokens": 10, "output_tokens": 5, "cache_read_input_tokens": 3, "cache_creation_input_tokens": 2}
}`

func TestSendMessageResponse(t *testing.T) {
	m := newTestModel(t, &fakeAPI{body: toolUseResponse}, 0)

	got, err := m.SendMessage(context.Background(), Request{
		Model: "test-model", MaxTokens: 100,
		Messages: []Message{{Role: RoleUser, Content: []Block{Text{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	want := Response{
		Content: []Block{
			Thinking{Thinking: "check files", Signature: "sig_1"},
			RedactedThinking{Data: "opaque"},
			Text{Text: "Listing files."},
			ToolUse{ID: "tu_1", Name: "bash", Input: json.RawMessage(`{"command": "ls"}`)},
		},
		StopReason: StopToolUse,
		Usage:      Usage{InputTokens: 10, OutputTokens: 5, CacheReadTokens: 3, CacheWriteTokens: 2},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("SendMessage response mismatch (-want +got):\n%s", diff)
	}
}

func TestSendMessageRequest(t *testing.T) {
	api := &fakeAPI{body: toolUseResponse}
	m := newTestModel(t, api, 0)

	req := Request{
		Model:     "test-model",
		System:    "You are loom.",
		MaxTokens: 100,
		Tools: []ToolSpec{{
			Name:        "bash",
			Description: "Run a command",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"],"additionalProperties":false}`),
		}},
		Messages: []Message{
			{Role: RoleUser, Content: []Block{Text{Text: "list files"}}},
			{Role: RoleAssistant, Content: []Block{
				Thinking{Thinking: "check files", Signature: "sig_1"},
				RedactedThinking{Data: "opaque"},
				ToolUse{ID: "tu_1", Name: "bash", Input: json.RawMessage(`{"command":"ls"}`)},
				ToolUse{ID: "tu_2", Name: "bash"},
			}},
			{Role: RoleUser, Content: []Block{
				ToolResult{ToolUseID: "tu_1", Content: "a.go"},
				ToolResult{ToolUseID: "tu_2", Content: "boom", IsError: true},
			}},
		},
	}
	if _, err := m.SendMessage(context.Background(), req); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	const want = `{
		"model": "test-model",
		"max_tokens": 100,
		"cache_control": {"type": "ephemeral"},
		"system": [{"type": "text", "text": "You are loom."}],
		"tools": [{
			"name": "bash",
			"description": "Run a command",
			"input_schema": {
				"type": "object",
				"properties": {"command": {"type": "string"}},
				"required": ["command"],
				"additionalProperties": false
			}
		}],
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "list files"}]},
			{"role": "assistant", "content": [
				{"type": "thinking", "thinking": "check files", "signature": "sig_1"},
				{"type": "redacted_thinking", "data": "opaque"},
				{"type": "tool_use", "id": "tu_1", "name": "bash", "input": {"command": "ls"}},
				{"type": "tool_use", "id": "tu_2", "name": "bash", "input": {}}
			]},
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "tu_1", "content": [{"type": "text", "text": "a.go"}], "is_error": false},
				{"type": "tool_result", "tool_use_id": "tu_2", "content": [{"type": "text", "text": "boom"}], "is_error": true}
			]}
		]
	}`
	if diff := cmp.Diff(decode(t, want), decode(t, string(api.lastBody))); diff != "" {
		t.Errorf("request body mismatch (-want +got):\n%s", diff)
	}
}

func TestSendMessageRetriesOverloaded(t *testing.T) {
	api := &fakeAPI{statuses: []int{529}, body: toolUseResponse}
	m := newTestModel(t, api, 1)

	if _, err := m.SendMessage(context.Background(), Request{Model: "m", MaxTokens: 1}); err != nil {
		t.Fatalf("SendMessage after one 529: %v", err)
	}
	if got := api.calls.Load(); got != 2 {
		t.Errorf("API calls = %d, want 2", got)
	}
}

func TestSendMessageAPIError(t *testing.T) {
	m := newTestModel(t, &fakeAPI{statuses: []int{529}}, 0)

	_, err := m.SendMessage(context.Background(), Request{Model: "m", MaxTokens: 1})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("SendMessage error = %v, want *APIError", err)
	}
	if apiErr.StatusCode != 529 || apiErr.Type != "overloaded_error" || apiErr.RequestID != "req_test" {
		t.Errorf("APIError = %+v, want status 529, type overloaded_error, request req_test", apiErr)
	}
}

func TestSendMessageUnexpectedResponse(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "unknown stop reason",
			body: `{"type":"message","role":"assistant","content":[],"stop_reason":"new_reason","usage":{}}`,
		},
		{
			name: "unknown block type",
			body: `{"type":"message","role":"assistant","content":[{"type":"hologram"}],"stop_reason":"end_turn","usage":{}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestModel(t, &fakeAPI{body: tt.body}, 0)
			_, err := m.SendMessage(context.Background(), Request{Model: "m", MaxTokens: 1})
			if !errors.Is(err, ErrUnexpectedResponse) {
				t.Errorf("SendMessage error = %v, want ErrUnexpectedResponse", err)
			}
		})
	}
}

func TestSendMessageRejectsBadInput(t *testing.T) {
	m := newTestModel(t, &fakeAPI{body: toolUseResponse}, 0)
	tests := []struct {
		name string
		req  Request
	}{
		{name: "unknown role", req: Request{Messages: []Message{{Role: "system"}}}},
		{name: "invalid tool schema", req: Request{Tools: []ToolSpec{{Name: "x", InputSchema: json.RawMessage(`{`)}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := m.SendMessage(context.Background(), tt.req); err == nil {
				t.Error("SendMessage error = nil, want error")
			}
		})
	}
}

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("decode %s: %v", s, err)
	}
	return v
}
