// Package prompt builds the system prompt from ordered sections. Sections that
// never change come first, so the cached prefix stays stable across turns and
// sessions.
package prompt

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type Section struct {
	Title string
	Text  string
}

// InstructionFiles are read from the workspace root, in this order.
var InstructionFiles = []string{"AGENTS.md", "CLAUDE.md"}

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

func Identity() Section {
	return Section{Text: "You are loom, a coding agent. You work in a software project using the tools provided."}
}

func Guidelines() Section {
	return Section{Title: "How to work", Text: `- Inspect before you change things: read the relevant code and run commands to understand the state.
- For work with several steps, keep a todo list with the todo tool and update it as you go: one item in_progress at a time, mark items completed as soon as they are done.
- Make the smallest change that solves the task, in the style of the surrounding code.
- Verify your changes, for example by building and running the tests.
- Finish with a short summary of what you did and anything left undone.`}
}

func Environment(workdir string) Section {
	return Section{Title: "Environment", Text: fmt.Sprintf("Working directory: %s\nPlatform: %s/%s", workdir, runtime.GOOS, runtime.GOARCH)}
}

// Instruction is one project instruction file that was found.
type Instruction struct {
	Path string
	Text string
}

// LoadInstructions reads the instruction files in dir that exist.
func LoadInstructions(dir string) ([]Instruction, error) {
	var found []Instruction
	for _, name := range InstructionFiles {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path) //nolint:gosec // G304: fixed file names in the workspace
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		found = append(found, Instruction{Path: path, Text: string(data)})
	}
	return found, nil
}

func Instructions(found []Instruction) Section {
	var parts []string
	for _, in := range found {
		parts = append(parts, "Contents of "+in.Path+":\n\n"+strings.TrimSpace(in.Text))
	}
	return Section{Title: "Project instructions", Text: strings.Join(parts, "\n\n")}
}
