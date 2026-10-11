package tool

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/xzwache/loom/internal/llm"
)

type namedTool string

func (n namedTool) Spec() llm.ToolSpec                                   { return llm.ToolSpec{Name: string(n)} }
func (n namedTool) Run(context.Context, json.RawMessage) (string, error) { return "", nil }

func TestRegistry(t *testing.T) {
	r, err := NewRegistry(namedTool("write"), namedTool("bash"), namedTool("read"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	var names []string
	for _, s := range r.Specs() {
		names = append(names, s.Name)
	}
	if diff := cmp.Diff([]string{"bash", "read", "write"}, names); diff != "" {
		t.Errorf("Specs() order mismatch (-want +got):\n%s", diff)
	}
	if _, ok := r.Get("read"); !ok {
		t.Error(`Get("read") not found`)
	}
	if _, ok := r.Get("nope"); ok {
		t.Error(`Get("nope") found`)
	}
}

func TestRegistryRejects(t *testing.T) {
	if _, err := NewRegistry(namedTool("bash"), namedTool("bash")); err == nil {
		t.Error("duplicate names: error = nil, want error")
	}
	if _, err := NewRegistry(namedTool("")); err == nil {
		t.Error("empty name: error = nil, want error")
	}
}
