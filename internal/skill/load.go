package skill

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Load reads the skills in each root: every <root>/<name>/SKILL.md. Roots
// that do not exist are skipped. A skill in a later root replaces one with
// the same name, so project skills override user skills.
func Load(roots ...string) ([]Skill, error) {
	byName := map[string]Skill{}
	for _, root := range roots {
		skills, err := loadRoot(root)
		if err != nil {
			return nil, err
		}
		for _, s := range skills {
			byName[s.Name] = s
		}
	}

	all := make([]Skill, 0, len(byName))
	for _, s := range byName {
		all = append(all, s)
	}
	slices.SortFunc(all, func(a, b Skill) int { return strings.Compare(a.Name, b.Name) })
	return all, nil
}

func loadRoot(root string) ([]Skill, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read skills: %w", err)
	}

	var skills []Skill
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		data, err := os.ReadFile(filepath.Join(dir, FileName)) //nolint:gosec // G304: skill folders are chosen by the user
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read skill: %w", err)
		}
		s, err := Parse(dir, data)
		if err != nil {
			return nil, err
		}
		skills = append(skills, s)
	}
	return skills, nil
}
