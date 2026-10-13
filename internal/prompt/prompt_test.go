package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuild(t *testing.T) {
	got := Build(Section{Text: "intro"}, Section{Title: "Empty"}, Section{Title: "Rules", Text: " be brief \n"})
	if want := "intro\n\n# Rules\n\nbe brief"; got != want {
		t.Errorf("Build = %q, want %q", got, want)
	}
}

func TestInstructions(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("Use tabs.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	found, err := LoadInstructions(dir)
	if err != nil || len(found) != 1 {
		t.Fatalf("LoadInstructions = %+v, %v; want CLAUDE.md", found, err)
	}
	s := Instructions(found)
	if !strings.Contains(s.Text, "CLAUDE.md:\n\nUse tabs.") {
		t.Errorf("Instructions = %q", s.Text)
	}
	if Instructions(nil).Text != "" {
		t.Error("Instructions without files is not empty")
	}
}
