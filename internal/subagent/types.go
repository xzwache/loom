package subagent

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Type is a kind of subagent, defined by a markdown file: front matter with
// name, description and optional tools and model, then the system prompt.
type Type struct {
	Name        string
	Description string
	// Tools limits the type to these tools; empty means every subagent tool.
	Tools []string
	// Model overrides the subagent model.
	Model  string
	Prompt string
	Path   string
}

//go:embed agents/*.md
var builtin embed.FS

var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// LoadTypes returns the built-in types and those in dirs, sorted by name. A
// type in a later dir replaces one with the same name.
func LoadTypes(dirs ...string) ([]Type, error) {
	byName := map[string]Type{}
	add := func(fsys fs.FS, prefix string) error {
		files, err := fs.Glob(fsys, "*.md")
		if err != nil {
			return err
		}
		for _, f := range files {
			data, err := fs.ReadFile(fsys, f)
			if err != nil {
				return err
			}
			t, err := ParseType(filepath.Join(prefix, f), data)
			if err != nil {
				return err
			}
			byName[t.Name] = t
		}
		return nil
	}

	agents, _ := fs.Sub(builtin, "agents")
	if err := add(agents, "builtin"); err != nil {
		return nil, err
	}
	for _, dir := range dirs {
		if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err := add(os.DirFS(dir), dir); err != nil {
			return nil, err
		}
	}

	types := make([]Type, 0, len(byName))
	for _, t := range byName {
		types = append(types, t)
	}
	slices.SortFunc(types, func(a, b Type) int { return strings.Compare(a.Name, b.Name) })
	return types, nil
}

// ParseType reads a type file. Unknown front matter keys are ignored, so
// files written for Claude Code load too.
func ParseType(path string, data []byte) (Type, error) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return Type{}, fmt.Errorf("%s: missing front matter", path)
	}
	head, body, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		return Type{}, fmt.Errorf("%s: unterminated front matter", path)
	}

	t := Type{Path: path, Prompt: strings.TrimSpace(body)}
	for line := range strings.SplitSeq(head, "\n") {
		key, value, _ := strings.Cut(line, ":")
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "name":
			t.Name = value
		case "description":
			t.Description = value
		case "model":
			t.Model = value
		case "tools":
			for name := range strings.SplitSeq(strings.Trim(value, "[]"), ",") {
				if name = strings.ToLower(strings.TrimSpace(name)); name != "" {
					t.Tools = append(t.Tools, name)
				}
			}
		}
	}

	switch {
	case !validName.MatchString(t.Name):
		return Type{}, fmt.Errorf("%s: name %q must be lowercase letters, digits and dashes", path, t.Name)
	case t.Description == "":
		return Type{}, fmt.Errorf("%s: description is empty", path)
	case t.Prompt == "":
		return Type{}, fmt.Errorf("%s: prompt is empty", path)
	}
	return t, nil
}
