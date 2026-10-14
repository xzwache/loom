package subagent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestParseType(t *testing.T) {
	// A Claude Code agent file: capitalized tools, extra keys.
	data := "---\r\nname: reviewer\r\ndescription: Reviews a diff.\r\ntools: Read, Glob\r\nmodel: small\r\ncolor: blue\r\n---\r\nYou review.\r\n"
	got, err := ParseType("reviewer.md", []byte(data))
	if err != nil {
		t.Fatal(err)
	}
	want := Type{Name: "reviewer", Description: "Reviews a diff.", Tools: []string{"read", "glob"}, Model: "small", Prompt: "You review.", Path: "reviewer.md"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ParseType mismatch (-want +got):\n%s", diff)
	}
}

func TestParseTypeErrors(t *testing.T) {
	tests := map[string]string{
		"no front matter": "You review.",
		"unterminated":    "---\nname: x\n",
		"bad name":        "---\nname: Code Reviewer\ndescription: d\n---\np",
		"no description":  "---\nname: x\n---\np",
		"no prompt":       "---\nname: x\ndescription: d\n---\n",
	}
	for name, data := range tests {
		if _, err := ParseType("x.md", []byte(data)); err == nil {
			t.Errorf("%s: ParseType succeeded", name)
		}
	}
}

func TestLoadTypes(t *testing.T) {
	user, project := t.TempDir(), t.TempDir()
	write := func(dir, name, text string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(user, "reviewer.md", "---\nname: reviewer\ndescription: user\n---\np")
	write(project, "reviewer.md", "---\nname: reviewer\ndescription: project\n---\np")
	write(project, "explore.md", "---\nname: explore\ndescription: custom explore\n---\np")

	types, err := LoadTypes(user, project, filepath.Join(project, "missing"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, ty := range types {
		got = append(got, ty.Name+"="+ty.Description)
	}
	if want := "explore=custom explore general-purpose=Any"; !strings.HasPrefix(strings.Join(got, " "), want) || !strings.HasSuffix(got[2], "reviewer=project") {
		t.Errorf("LoadTypes = %v", got)
	}
}
