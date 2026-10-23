package tool

import (
	"context"
	"encoding/json"
	"slices"
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

func TestCovers(t *testing.T) {
	tests := []struct {
		entry, name string
		want        bool
	}{
		{"bash", "bash", true},
		{"bash", "bash2", false},
		{"mcp__github", "mcp__github__create_issue", true},
		{"mcp__github__create_issue", "mcp__github__create_issue", true},
		{"mcp__git", "mcp__github__list", false},
		{"mcp__github__create", "mcp__github__create_issue", false},
		{"mcp__", "mcp__github__list", false},
	}
	for _, tt := range tests {
		if got := Covers(tt.entry, tt.name); got != tt.want {
			t.Errorf("Covers(%q, %q) = %v, want %v", tt.entry, tt.name, got, tt.want)
		}
	}
}

func TestSelect(t *testing.T) {
	r, err := NewRegistry(namedTool("read"), namedTool("bash"), namedTool("mcp__docs__search"), namedTool("mcp__docs__get"), namedTool("mcp__gh__list"))
	if err != nil {
		t.Fatal(err)
	}
	sub, unmatched := r.Select("read", "mcp__docs", "mcp__slack", "glob")
	var got []string
	for _, s := range sub.Specs() {
		got = append(got, s.Name)
	}
	slices.Sort(got)
	if diff := cmp.Diff([]string{"mcp__docs__get", "mcp__docs__search", "read"}, got); diff != "" {
		t.Errorf("selected (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"mcp__slack", "glob"}, unmatched); diff != "" {
		t.Errorf("unmatched (-want +got):\n%s", diff)
	}
}
