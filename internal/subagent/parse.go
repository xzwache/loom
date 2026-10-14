package subagent

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/xzwache/loom/internal/frontmatter"
)

var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ParseType reads a type file. Unknown front matter keys are ignored, so
// files written for Claude Code load too.
func ParseType(path string, data []byte) (Type, error) {
	doc, err := frontmatter.Parse(data)
	if err != nil {
		return Type{}, fmt.Errorf("%s: %w", path, err)
	}
	t := Type{
		Name:        doc.Fields["name"],
		Description: doc.Fields["description"],
		Model:       doc.Fields["model"],
		Prompt:      doc.Body,
		Path:        path,
	}
	for _, name := range frontmatter.List(doc.Fields["tools"]) {
		t.Tools = append(t.Tools, strings.ToLower(name))
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
