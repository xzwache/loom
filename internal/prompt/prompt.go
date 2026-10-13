// Package prompt builds the system prompt from ordered sections. Sections that
// never change come first, so the cached prefix stays stable across turns and
// sessions.
package prompt

import (
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
)

var (
	//go:embed system.md
	systemMD string
	//go:embed subagent.md
	subagentMD string
)

type Section struct {
	Title string
	Text  string
}

// Build joins the non-empty sections, each under a "# Title" heading.
func Build(sections ...Section) string {
	var parts []string
	for _, s := range sections {
		text := strings.TrimSpace(s.Text)
		if text == "" {
			continue
		}
		if s.Title != "" {
			text = "# " + s.Title + "\n\n" + text
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, "\n\n")
}

// System is the main agent's fixed prompt; Subagent is the one for agents it starts.
func System() Section   { return Section{Text: systemMD} }
func Subagent() Section { return Section{Text: subagentMD} }

func Environment(workdir string) Section {
	return Section{Title: "Environment", Text: fmt.Sprintf("Working directory: %s\nPlatform: %s/%s", workdir, runtime.GOOS, runtime.GOARCH)}
}

// DefaultInstructionFiles are read in this order, so the loom-only file comes
// last and wins on conflicts.
var DefaultInstructionFiles = []string{"AGENTS.md", "CLAUDE.md", "LOOM.md"}

const (
	SourceUser    = "user"
	SourceProject = "project"
)

type Instruction struct {
	Path   string
	Source string
	Text   string
}

// Loader finds instruction files: the user's in UserDir first, then those in
// each directory from the repository root down to the workspace.
type Loader struct {
	Names   []string
	UserDir string
}

// importOnly matches a file that only imports another, like a CLAUDE.md
// holding "@AGENTS.md".
var importOnly = regexp.MustCompile(`^@(\S+)$`)

// Load skips a file already loaded under another name: a symlink, a copy, or
// a file that only imports it.
func (l Loader) Load(workdir string) ([]Instruction, error) {
	type dir struct{ path, source string }
	var dirs []dir
	if l.UserDir != "" {
		dirs = append(dirs, dir{l.UserDir, SourceUser})
	}
	for _, d := range projectDirs(workdir) {
		dirs = append(dirs, dir{d, SourceProject})
	}

	var found []Instruction
	seenPath, seenText := map[string]bool{}, map[string]bool{}
	for _, d := range dirs {
		for _, name := range l.Names {
			path := filepath.Join(d.path, name)
			data, err := os.ReadFile(path) //nolint:gosec // G304: configured file names in known directories
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("read %s: %w", path, err)
			}
			text := strings.TrimSpace(string(data))
			real := realPath(path)
			if m := importOnly.FindStringSubmatch(text); m != nil && seenPath[realPath(filepath.Join(d.path, m[1]))] {
				continue
			}
			if text == "" || seenPath[real] || seenText[text] {
				continue
			}
			seenPath[real], seenText[text] = true, true
			found = append(found, Instruction{Path: path, Source: d.source, Text: text})
		}
	}
	return found, nil
}

// projectDirs lists the directories from the repository root containing
// workdir down to workdir, or only workdir outside a repository.
func projectDirs(workdir string) []string {
	dirs := []string{workdir}
	for d := workdir; ; {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			slices.Reverse(dirs)
			return dirs
		}
		parent := filepath.Dir(d)
		if parent == d {
			return []string{workdir}
		}
		d = parent
		dirs = append(dirs, d)
	}
}

func realPath(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return filepath.Clean(path)
}

func Instructions(found []Instruction) Section {
	var parts []string
	for _, in := range found {
		parts = append(parts, "Contents of "+in.Path+":\n\n"+in.Text)
	}
	return Section{Title: "Project instructions", Text: strings.Join(parts, "\n\n")}
}
