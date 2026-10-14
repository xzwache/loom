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

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func names(found []Instruction) []string {
	var out []string
	for _, in := range found {
		out = append(out, in.Source+":"+filepath.Base(filepath.Dir(in.Path))+"/"+filepath.Base(in.Path))
	}
	return out
}

func TestLoad(t *testing.T) {
	root := t.TempDir()
	user := filepath.Join(root, "config")
	repo := filepath.Join(root, "repo")
	work := filepath.Join(repo, "svc")
	write(t, filepath.Join(user, "LOOM.md"), "Be brief.")
	write(t, filepath.Join(repo, ".git", "HEAD"), "ref")
	write(t, filepath.Join(repo, "AGENTS.md"), "Use tabs.")
	write(t, filepath.Join(repo, "CLAUDE.md"), "@AGENTS.md\n")
	write(t, filepath.Join(work, "AGENTS.md"), "Run make check.")
	write(t, filepath.Join(work, "CLAUDE.md"), "Use tabs.")
	write(t, filepath.Join(work, "LOOM.md"), "Prefer table tests.")
	write(t, filepath.Join(work, "EMPTY.md"), " \n")
	if err := os.Symlink("AGENTS.md", filepath.Join(work, "GEMINI.md")); err != nil {
		t.Fatal(err)
	}

	l := Loader{Names: []string{"AGENTS.md", "CLAUDE.md", "GEMINI.md", "EMPTY.md", "LOOM.md"}, UserDir: user}
	found, err := l.Load(work)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"user:config/LOOM.md", "project:repo/AGENTS.md", "project:svc/AGENTS.md", "project:svc/LOOM.md"}
	if got := names(found); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("Load = %v, want %v", got, want)
	}
	if s := Instructions(found).Text; !strings.Contains(s, "AGENTS.md:\n\nRun make check.") {
		t.Errorf("Instructions = %q", s)
	}
	if Instructions(nil).Text != "" {
		t.Error("Instructions without files is not empty")
	}
}

func TestLoadOutsideRepository(t *testing.T) {
	parent := t.TempDir()
	work := filepath.Join(parent, "work")
	write(t, filepath.Join(parent, "AGENTS.md"), "parent")
	write(t, filepath.Join(work, "AGENTS.md"), "work")
	found, err := Loader{Names: DefaultInstructionFiles}.Load(work)
	if err != nil || len(found) != 1 || found[0].Text != "work" {
		t.Errorf("Load = %+v, %v; want only the workspace file", found, err)
	}
}
