package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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
	w.Header().Set("Request-Id", "req_test")
	if n <= len(f.statuses) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.statuses[n-1])
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"overloaded_error","message":"try later"}}`)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = io.WriteString(w, f.body)
}

// sse writes server events the way the API streams them; each one names
// its type in a "type" field.
func sse(t *testing.T, events ...string) string {
	t.Helper()
	var b strings.Builder
	for _, e := range events {
		var head struct{ Type string }
		if err := json.Unmarshal([]byte(e), &head); err != nil {
			t.Fatalf("bad test event %s: %v", e, err)
		}
		fmt.Fprintf(&b, "event: %s\ndata: %s\n\n", head.Type, e)
	}
	return b.String()
}

func newTestModel(t *testing.T, api *fakeAPI, maxRetries int) *Anthropic {
	t.Helper()
	api.t = t
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	return NewAnthropic(AnthropicOptions{APIKey: "test-key", BaseURL: srv.URL, MaxRetries: maxRetries})
}

// toolUseEvents is a whole streamed reply: thinking, redacted thinking,
// text and a tool call, each in pieces, with a ping in between.
var toolUseEvents = []string{
	`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"test-model","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":1,"cache_read_input_tokens":3,"cache_creation_input_tokens":2}}}`,
	`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"check "}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"files"}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig_1"}}`,
	`{"type":"content_block_stop","index":0}`,
	`{"type":"content_block_start","index":1,"content_block":{"type":"redacted_thinking","data":"opaque"}}`,
	`{"type":"content_block_stop","index":1}`,
	`{"type":"ping"}`,
	`{"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`,
	`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"Listing "}}`,
	`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"files."}}`,
	`{"type":"content_block_stop","index":2}`,
	`{"type":"content_block_start","index":3,"content_block":{"type":"tool_use","id":"tu_1","name":"bash","input":{}}}`,
	`{"type":"content_block_delta","index":3,"delta":{"type":"input_json_delta","partial_json":"{\"command\": "}}`,
	`{"type":"content_block_delta","index":3,"delta":{"type":"input_json_delta","partial_json":"\"ls\"}"}}`,
	`{"type":"content_block_stop","index":3}`,
	`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":5}}`,
	`{"type":"message_stop"}`,
}

func endTurnEvents(text string) []string {
	return []string{
		`{"type":"message_start","message":{"id":"msg_2","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":1,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":` + strconv.Quote(text) + `}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
		`{"type":"message_stop"}`,
	}
}

func TestStreamResponse(t *testing.T) {
	m := newTestModel(t, &fakeAPI{body: sse(t, toolUseEvents...)}, 0)
	var deltas []string

	got, err := Collect(m.Stream(context.Background(), Request{
		Model: "test-model", MaxTokens: 100,
		Messages: []Message{{Role: RoleUser, Content: []Block{Text{Text: "hi"}}}},
	}), func(ev StreamEvent) {
		switch ev := ev.(type) {
		case TextDelta:
			deltas = append(deltas, "text:"+ev.Text)
		case ThinkingDelta:
			deltas = append(deltas, "thinking:"+ev.Thinking)
		case ToolInputDelta:
			deltas = append(deltas, "input:"+ev.JSON)
		}
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	want := Response{
		Content: []Block{
			Thinking{Thinking: "check files", Signature: "sig_1"},
			RedactedThinking{Data: "opaque"},
			ModelText("Listing files."),
			ToolUse{ID: "tu_1", Name: "bash", Input: json.RawMessage(`{"command": "ls"}`)},
		},
		StopReason: StopToolUse,
		Usage:      Usage{InputTokens: 10, OutputTokens: 5, CacheReadTokens: 3, CacheWriteTokens: 2},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("response mismatch (-want +got):\n%s", diff)
	}
	wantDeltas := []string{"thinking:check ", "thinking:files", "text:Listing ", "text:files.", `input:{"command": `, `input:"ls"}`}
	if diff := cmp.Diff(wantDeltas, deltas); diff != "" {
		t.Errorf("deltas mismatch (-want +got):\n%s", diff)
	}
}

func TestStreamRequest(t *testing.T) {
	api := &fakeAPI{body: sse(t, toolUseEvents...)}
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
			{Role: RoleUser, Content: []Block{UserText("list files"), SystemText(KindMemoryHint, "read tests-need-db")}},
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
	if _, err := Send(context.Background(), m, req); err != nil {
		t.Fatalf("Send: %v", err)
	}

	const want = `{
		"model": "test-model",
		"max_tokens": 100,
		"stream": true,
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
			{"role": "user", "content": [
				{"type": "text", "text": "list files"},
				{"type": "text", "text": "<loom-note kind=\"memory_hint\">\nread tests-need-db\n</loom-note>"}
			]},
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

func TestStreamRetriesOverloaded(t *testing.T) {
	api := &fakeAPI{statuses: []int{529}, body: sse(t, endTurnEvents("ok")...)}
	m := newTestModel(t, api, 1)

	if _, err := Send(context.Background(), m, Request{Model: "m", MaxTokens: 1}); err != nil {
		t.Fatalf("Send after one 529: %v", err)
	}
	if got := api.calls.Load(); got != 2 {
		t.Errorf("API calls = %d, want 2", got)
	}
}

func TestStreamAPIError(t *testing.T) {
	m := newTestModel(t, &fakeAPI{statuses: []int{529}}, 0)

	_, err := Send(context.Background(), m, Request{Model: "m", MaxTokens: 1})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("Send error = %v, want *APIError", err)
	}
	if apiErr.StatusCode != 529 || apiErr.Type != "overloaded_error" || apiErr.RequestID != "req_test" {
		t.Errorf("APIError = %+v, want status 529, type overloaded_error, request req_test", apiErr)
	}
}

func TestStreamUnexpectedResponse(t *testing.T) {
	start := endTurnEvents("x")[0]
	tests := []struct {
		name   string
		events []string
	}{
		{
			name: "unknown stop reason",
			events: []string{
				start,
				`{"type":"message_delta","delta":{"stop_reason":"new_reason"},"usage":{"output_tokens":1}}`,
				`{"type":"message_stop"}`,
			},
		},
		{
			name: "unknown block type",
			events: []string{
				start,
				`{"type":"content_block_start","index":0,"content_block":{"type":"hologram"}}`,
			},
		},
		{
			name: "unknown delta type",
			events: []string{
				start,
				`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"smell_delta"}}`,
			},
		},
		{
			name:   "stream cut before the end",
			events: endTurnEvents("x")[:3],
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestModel(t, &fakeAPI{body: sse(t, tt.events...)}, 0)
			_, err := Send(context.Background(), m, Request{Model: "m", MaxTokens: 1})
			if !errors.Is(err, ErrUnexpectedResponse) {
				t.Errorf("Send error = %v, want ErrUnexpectedResponse", err)
			}
		})
	}
}

func TestStreamErrorEvent(t *testing.T) {
	body := sse(t, endTurnEvents("x")[:3]...) +
		"event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"try later\"}}\n\n"
	m := newTestModel(t, &fakeAPI{body: body}, 0)

	_, err := Send(context.Background(), m, Request{Model: "m", MaxTokens: 1})
	if err == nil || !strings.Contains(err.Error(), "overloaded_error") {
		t.Errorf("Send error = %v, want the overloaded error from the stream", err)
	}
}

func TestStreamCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse(t, endTurnEvents("x")[:3]...))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	m := NewAnthropic(AnthropicOptions{APIKey: "k", BaseURL: srv.URL})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err := Collect(m.Stream(ctx, Request{Model: "m", MaxTokens: 1}), func(StreamEvent) { cancel() })
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Collect error = %v, want context.Canceled", err)
	}
}

func TestStreamRejectsBadInput(t *testing.T) {
	m := newTestModel(t, &fakeAPI{body: sse(t, toolUseEvents...)}, 0)
	tests := []struct {
		name string
		req  Request
	}{
		{name: "unknown role", req: Request{Messages: []Message{{Role: "system"}}}},
		{name: "invalid tool schema", req: Request{Tools: []ToolSpec{{Name: "x", InputSchema: json.RawMessage(`{`)}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Send(context.Background(), m, tt.req); err == nil {
				t.Error("Send error = nil, want error")
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
