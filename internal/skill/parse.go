package skill

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/xzwache/loom/internal/frontmatter"
)

var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

func Parse(dir string, data []byte) (Skill, error) {
	path := filepath.Join(dir, FileName)
	doc, err := frontmatter.Parse(data)
	if err != nil {
		return Skill{}, fmt.Errorf("%s: %w", path, err)
	}

	s := Skill{
		Name:         doc.Fields["name"],
		Description:  doc.Fields["description"],
		Instructions: doc.Body,
		Dir:          dir,
	}
	if s.Name == "" {
		s.Name = filepath.Base(dir)
	}
	if s.Description == "" {
		s.Description = firstParagraph(doc.Body)
	}

	switch {
	case !validName.MatchString(s.Name):
		return Skill{}, fmt.Errorf("%s: name %q must be lowercase letters, digits and dashes", path, s.Name)
	case s.Description == "":
		return Skill{}, fmt.Errorf("%s: description is empty", path)
	case s.Instructions == "":
		return Skill{}, fmt.Errorf("%s: instructions are empty", path)
	}
	return s, nil
}

// firstParagraph returns the first block of text that is not a heading.
func firstParagraph(body string) string {
	for para := range strings.SplitSeq(body, "\n\n") {
		if para = strings.TrimSpace(para); para != "" && !strings.HasPrefix(para, "#") {
			return strings.Join(strings.Fields(para), " ")
		}
	}
	return ""
}
