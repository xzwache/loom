package llm

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestMessageJSONRoundTrip(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: []Block{Text{Text: "hi"}}},
		{Role: RoleAssistant, Content: []Block{
			Thinking{Thinking: "plan", Signature: "sig"},
			RedactedThinking{Data: "opaque"},
			Text{Text: "running"},
			ToolUse{ID: "t1", Name: "bash", Input: json.RawMessage(`{"command":"ls"}`)},
		}},
		{Role: RoleUser, Content: []Block{ToolResult{ToolUseID: "t1", Content: "boom", IsError: true}}},
	}
	data, err := json.Marshal(msgs)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	var got []Message
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}
	if diff := cmp.Diff(msgs, got); diff != "" {
		t.Errorf("round trip mismatch (-want +got):\n%s", diff)
	}
}

func TestMessageJSONUnknownBlock(t *testing.T) {
	var m Message
	err := json.Unmarshal([]byte(`{"role":"user","content":[{"type":"image"}]}`), &m)
	if err == nil || !strings.Contains(err.Error(), "image") {
		t.Errorf("Unmarshal error = %v, want unknown block type", err)
	}
}
