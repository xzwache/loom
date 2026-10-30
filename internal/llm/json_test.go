package llm

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestMessageJSONRoundTrip(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: []Block{UserText("hi"), SystemText(KindMemoryHint, "hint")}},
		{Role: RoleAssistant, Content: []Block{
			Thinking{Thinking: "plan", Signature: "sig"},
			RedactedThinking{Data: "opaque"},
			ModelText("running"),
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

func TestTextOrigins(t *testing.T) {
	// A session saved before texts had an origin takes it from the role.
	var m Message
	if err := json.Unmarshal([]byte(`{"role":"assistant","content":[{"type":"text","text":"hi"}]}`), &m); err != nil {
		t.Fatal(err)
	}
	if m.Content[0] != ModelText("hi") {
		t.Errorf("old assistant text = %+v, want model text", m.Content[0])
	}

	bad := map[string]Message{
		"no origin":        {Role: RoleUser, Content: []Block{Text{Text: "x"}}},
		"user says model":  {Role: RoleUser, Content: []Block{ModelText("x")}},
		"assistant system": {Role: RoleAssistant, Content: []Block{SystemText(KindSummary, "x")}},
		"system no kind":   {Role: RoleUser, Content: []Block{Text{Text: "x", Origin: OriginSystem}}},
		"user with kind":   {Role: RoleUser, Content: []Block{Text{Text: "x", Origin: OriginUser, Kind: KindSummary}}},
	}
	for name, m := range bad {
		if m.Check() == nil {
			t.Errorf("%s: Check passed", name)
		}
	}
	mixed := Message{Role: RoleUser, Content: []Block{UserText("a"), SystemText(KindHookContext, "b"), ToolResult{}}}
	if err := mixed.Check(); err != nil {
		t.Errorf("Check(user + system) = %v", err)
	}
	if got := mixed.Texts(OriginUser); len(got) != 1 || got[0].Text != "a" {
		t.Errorf("Texts(user) = %+v", got)
	}
}
