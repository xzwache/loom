package subagent

import (
	"embed"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

//go:embed agents/*.md
var builtin embed.FS

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
