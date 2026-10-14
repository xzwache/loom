package skill

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSkill creates root/name/SKILL.md and any extra files, given as
// path → content relative to the skill's folder.
func writeSkill(t *testing.T, root, name, skillMD string, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	files[FileName] = skillMD
	for path, content := range files {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestParse(t *testing.T) {
	// A Claude Code skill: extra keys such as allowed-tools are ignored.
	got, err := Parse("/skills/pdf", []byte("---\nname: pdf\ndescription: Fill PDF forms.\nallowed-tools: Read, Bash\n---\n# PDF\n\nSteps."))
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "pdf" || got.Description != "Fill PDF forms." || got.Instructions != "# PDF\n\nSteps." || got.Dir != "/skills/pdf" {
		t.Errorf("Parse = %+v", got)
	}
}

func TestParseDefaults(t *testing.T) {
	got, err := Parse("/skills/release-notes", []byte("---\n---\n# Release notes\n\nWrite release\nnotes from git log.\n\nMore."))
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "release-notes" || got.Description != "Write release notes from git log." {
		t.Errorf("Parse = name %q, description %q; want the folder name and first paragraph", got.Name, got.Description)
	}
}

func TestParseErrors(t *testing.T) {
	tests := map[string]string{
		"no front matter": "Steps.",
		"bad name":        "---\nname: PDF Forms\ndescription: d\n---\nSteps.",
		"no description":  "---\nname: x\n---\n# Only a heading",
		"no instructions": "---\nname: x\ndescription: d\n---\n",
	}
	for name, data := range tests {
		if _, err := Parse("/skills/x", []byte(data)); err == nil {
			t.Errorf("%s: Parse succeeded", name)
		}
	}
}

func TestLoad(t *testing.T) {
	user, project := t.TempDir(), t.TempDir()
	writeSkill(t, user, "pdf", "---\ndescription: user pdf\n---\nSteps.", map[string]string{})
	writeSkill(t, user, "deploy", "---\ndescription: deploy\n---\nSteps.", map[string]string{})
	writeSkill(t, project, "pdf", "---\ndescription: project pdf\n---\nSteps.", map[string]string{})
	if err := os.MkdirAll(filepath.Join(project, "not-a-skill"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "README.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	skills, err := Load(user, project, filepath.Join(project, "missing"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range skills {
		got = append(got, s.Name+"="+s.Description)
	}
	if want := "deploy=deploy pdf=project pdf"; strings.Join(got, " ") != want {
		t.Errorf("Load = %v, want %s", got, want)
	}
}

func TestLoadRejectsBrokenSkill(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "x", "no front matter", map[string]string{})
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), filepath.Join("x", FileName)) {
		t.Errorf("Load = %v, want an error naming the file", err)
	}
}

func TestTool(t *testing.T) {
	dir := writeSkill(t, t.TempDir(), "pdf", "---\ndescription: Fill PDF forms.\n---\nRun scripts/fill.py.", map[string]string{
		"forms.md":        "Field rules.",
		"scripts/fill.py": "print(1)",
		".cache/x":        "hidden",
	})
	s, err := Parse(dir, []byte("---\ndescription: Fill PDF forms.\n---\nRun scripts/fill.py."))
	if err != nil {
		t.Fatal(err)
	}
	var used []string
	tl := NewTool([]Skill{s}, func(_ context.Context, s Skill) { used = append(used, s.Name) })

	if spec := tl.Spec(); !strings.Contains(spec.Description, "- pdf: Fill PDF forms.") || !strings.Contains(string(spec.InputSchema), `"enum": ["pdf"]`) {
		t.Errorf("spec does not list the skill: %s\n%s", spec.Description, spec.InputSchema)
	}

	run := func(input string) (string, error) {
		return tl.Run(context.Background(), json.RawMessage(input))
	}
	out, err := run(`{"skill":"pdf"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, dir) || !strings.Contains(out, "forms.md, scripts/fill.py\n") || !strings.HasSuffix(out, "\nRun scripts/fill.py.") {
		t.Errorf("instructions = %q", out)
	}
	if len(used) != 1 {
		t.Errorf("onUse ran %d times, want 1", len(used))
	}

	if out, err := run(`{"skill":"pdf","file":"forms.md"}`); err != nil || out != "Field rules." {
		t.Errorf("file = %q, %v", out, err)
	}
	if len(used) != 1 {
		t.Error("reading a file counted as using the skill")
	}
	for _, bad := range []string{`{"skill":"pdf","file":"../outside"}`, `{"skill":"pdf","file":"missing.md"}`, `{"skill":"nope"}`, `{`} {
		if _, err := run(bad); err == nil {
			t.Errorf("Run(%s) succeeded", bad)
		}
	}
}
