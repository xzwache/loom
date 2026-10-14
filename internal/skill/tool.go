package skill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"

	"github.com/xzwache/loom/internal/llm"
	"github.com/xzwache/loom/internal/tool"
)

const (
	maxListedFiles = 100
	maxFileOutput  = 30_000
)

// Tool is the skill tool. Called with a skill name it returns the skill's
// instructions; called with a name and a file it returns that file from the
// skill's folder.
type Tool struct {
	skills map[string]Skill
	spec   llm.ToolSpec
	onUse  func(ctx context.Context, s Skill)
}

type input struct {
	Skill string `json:"skill"`
	File  string `json:"file"`
}

// NewTool returns the tool for skills. onUse, if set, runs each time the
// model loads a skill's instructions.
func NewTool(skills []Skill, onUse func(ctx context.Context, s Skill)) *Tool {
	t := &Tool{skills: map[string]Skill{}, onUse: onUse}
	var names, catalog []string
	for _, s := range skills {
		t.skills[s.Name] = s
		names = append(names, s.Name)
		catalog = append(catalog, "- "+s.Name+": "+s.Description)
	}
	t.spec = spec(names, catalog)
	return t
}

func spec(names, catalog []string) llm.ToolSpec {
	enum, _ := json.Marshal(names)
	return llm.ToolSpec{
		Name: tool.Skill,
		Description: "Load a skill: instructions for one kind of task. When the task matches a skill below, load it " +
			"before you start and follow it. Load a file the instructions mention by passing its path as file.\n\n" +
			"Skills:\n" + strings.Join(catalog, "\n"),
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"skill": {"type": "string", "enum": ` + string(enum) + `},
				"file": {"type": "string", "description": "A file in the skill's folder, relative to it; leave out to load the instructions"}
			},
			"required": ["skill"]
		}`),
	}
}

func (t *Tool) Spec() llm.ToolSpec { return t.spec }

func (t *Tool) Run(ctx context.Context, raw json.RawMessage) (string, error) {
	var in input
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	s, ok := t.skills[in.Skill]
	if !ok {
		return "", fmt.Errorf("unknown skill %q", in.Skill)
	}
	if in.File != "" {
		return readFile(s, in.File)
	}
	if t.onUse != nil {
		t.onUse(ctx, s)
	}
	return instructions(s)
}

// instructions returns the skill's instructions, preceded by where its folder
// is and which files it holds, so the model can use them.
func instructions(s Skill) (string, error) {
	files, err := listFiles(s.Dir)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Skill %q, from %s.\n", s.Name, s.Dir)
	if len(files) > 0 {
		fmt.Fprintf(&b, "Its files (load one with the file field, run scripts with bash from that folder): %s\n",
			strings.Join(files, ", "))
	}
	b.WriteString("\n" + s.Instructions)
	return b.String(), nil
}

// readFile reads a file inside the skill's folder; paths that leave it fail.
func readFile(s Skill, name string) (string, error) {
	root, err := os.OpenRoot(s.Dir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	data, err := root.ReadFile(name)
	if err != nil {
		return "", fmt.Errorf("skill %s: %w", s.Name, err)
	}
	return tool.Truncate(string(data), maxFileOutput), nil
}

// listFiles lists the skill's files other than SKILL.md, skipping hidden ones.
func listFiles(dir string) ([]string, error) {
	var files []string
	err := fs.WalkDir(os.DirFS(dir), ".", func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case path != "." && strings.HasPrefix(d.Name(), "."):
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		case d.IsDir() || path == FileName:
			return nil
		case len(files) == maxListedFiles:
			return errFileLimit
		}
		files = append(files, path)
		return nil
	})
	if err != nil && !errors.Is(err, errFileLimit) {
		return nil, fmt.Errorf("list skill files: %w", err)
	}
	slices.Sort(files)
	return files, nil
}

var errFileLimit = errors.New("too many files")
