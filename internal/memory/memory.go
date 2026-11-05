// Package memory keeps what loom learns across sessions: one markdown file per
// fact, in the user's folder only, so a repository cannot plant memories.
//
//	~/.config/loom/memory/
//	  global/                facts for every workspace
//	    INDEX.md             one line per memory, rewritten after each change
//	    short-commits.md
//	  projects/<key>/        facts for one workspace (key: folder name + hash of its path)
//	    INDEX.md
//	    tests-need-db.md
//
// A memory file has front matter and a body:
//
//	---
//	name: tests-need-db
//	description: "integration tests need `make db` running first"
//	type: project
//	---
//	Run `make db` before `go test ./...`; otherwise tests time out on connect.
//
// Only names and descriptions go into the system prompt (see Section); the
// model reads a memory's text with the memory tool when it is relevant.
package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/xzwache/loom/internal/frontmatter"
)

type Scope string

const (
	ScopeGlobal  Scope = "global"
	ScopeProject Scope = "project"
)

var Scopes = []Scope{ScopeGlobal, ScopeProject}

type Type string

const (
	TypeUser      Type = "user"
	TypeFeedback  Type = "feedback"
	TypeProject   Type = "project"
	TypeReference Type = "reference"
)

var Types = []Type{TypeUser, TypeFeedback, TypeProject, TypeReference}

const (
	IndexFile      = "INDEX.md"
	maxDescription = 200
	maxContent     = 4000
)

// validName also keeps "index" out, as index.md and INDEX.md are one file on
// case-insensitive file systems.
var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

type Memory struct {
	Scope       Scope
	Name        string
	Description string
	Type        Type
	Content     string
}

func (m Memory) Validate() error {
	errs := []error{checkKey(m.Scope, m.Name)}
	if !slices.Contains(Types, m.Type) {
		errs = append(errs, fmt.Errorf("type %q is not user, feedback, project or reference", m.Type))
	}
	switch d := m.Description; {
	case strings.TrimSpace(d) == "":
		errs = append(errs, errors.New("description is empty"))
	case strings.ContainsAny(d, "\r\n"):
		errs = append(errs, errors.New("description must be one line"))
	case len(d) > maxDescription:
		errs = append(errs, fmt.Errorf("description is longer than %d characters", maxDescription))
	}
	switch c := m.Content; {
	case strings.TrimSpace(c) == "":
		errs = append(errs, errors.New("content is empty"))
	case len(c) > maxContent:
		errs = append(errs, fmt.Errorf("content is longer than %d characters", maxContent))
	}
	return errors.Join(errs...)
}

func checkKey(scope Scope, name string) error {
	var errs []error
	if !slices.Contains(Scopes, scope) {
		errs = append(errs, fmt.Errorf("scope %q is not global or project", scope))
	}
	if !validName.MatchString(name) || name == "index" {
		errs = append(errs, fmt.Errorf("name %q must be lowercase letters, digits and dashes, at most 64, not \"index\"", name))
	}
	return errors.Join(errs...)
}

// Line is how a memory shows in the index and the prompt.
func (m Memory) Line() string {
	return fmt.Sprintf("- %s (%s): %s", m.Name, m.Type, m.Description)
}

// The description is always quoted: front matter then reads any one line back
// as written, quotes inside included.
func (m Memory) file() []byte {
	return fmt.Appendf(nil, "---\nname: %s\ndescription: \"%s\"\ntype: %s\n---\n%s\n",
		m.Name, m.Description, m.Type, strings.TrimSpace(m.Content))
}

func parse(scope Scope, name string, data []byte) (Memory, error) {
	doc, err := frontmatter.Parse(data)
	if err != nil {
		return Memory{}, err
	}
	m := Memory{
		Scope:       scope,
		Name:        doc.Fields["name"],
		Description: doc.Fields["description"],
		Type:        Type(doc.Fields["type"]),
		Content:     doc.Body,
	}
	if m.Name != name {
		return Memory{}, fmt.Errorf("name %q does not match the file name", m.Name)
	}
	return m, m.Validate()
}

// ProjectKey names a workspace's folders in loom's own storage (memory,
// tasks): its folder name, readable, and a hash of its full path, so two
// folders named alike stay apart.
func ProjectKey(workdir string) string {
	abs, err := filepath.Abs(workdir)
	if err != nil {
		abs = workdir
	}
	sum := sha256.Sum256([]byte(abs))
	base := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '-'
	}, filepath.Base(abs))
	return strings.Trim(base, "-.") + "-" + hex.EncodeToString(sum[:4])
}

// Store reads and writes the memory files of one workspace. It is safe for
// concurrent use, as subagents read while the main agent writes.
type Store struct {
	mu   sync.Mutex
	dirs map[Scope]string
}

// NewStore keeps memories under root, e.g. ~/.config/loom/memory.
func NewStore(root, workdir string) *Store {
	return &Store{dirs: map[Scope]string{
		ScopeGlobal:  filepath.Join(root, "global"),
		ScopeProject: filepath.Join(root, "projects", ProjectKey(workdir)),
	}}
}

func (s *Store) Dir(scope Scope) string { return s.dirs[scope] }

func (s *Store) Path(scope Scope, name string) string {
	return filepath.Join(s.dirs[scope], name+".md")
}

// List reads every memory, global first, each scope sorted by name. A file it
// cannot read or parse is left out and reported in problems.
func (s *Store) List() (mems []Memory, problems []error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, scope := range Scopes {
		got, errs := s.list(scope)
		mems, problems = append(mems, got...), append(problems, errs...)
	}
	return mems, problems
}

func (s *Store) list(scope Scope) ([]Memory, []error) {
	entries, err := os.ReadDir(s.dirs[scope])
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, []error{fmt.Errorf("read memory folder: %w", err)}
	}
	var mems []Memory
	var problems []error
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".md")
		if !ok || e.IsDir() || e.Name() == IndexFile || strings.HasPrefix(name, ".") {
			continue
		}
		m, err := s.read(scope, name)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		mems = append(mems, m)
	}
	return mems, problems
}

func (s *Store) Read(scope Scope, name string) (Memory, error) {
	if err := checkKey(scope, name); err != nil {
		return Memory{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read(scope, name)
}

func (s *Store) read(scope Scope, name string) (Memory, error) {
	path := s.Path(scope, name)
	data, err := os.ReadFile(path) //nolint:gosec // G304: a validated name in loom's own memory folder
	if errors.Is(err, fs.ErrNotExist) {
		return Memory{}, fmt.Errorf("no %s memory named %q", scope, name)
	}
	if err != nil {
		return Memory{}, err
	}
	m, err := parse(scope, name, data)
	if err != nil {
		return Memory{}, fmt.Errorf("memory %s: %w", path, err)
	}
	return m, nil
}

// Write saves m, replacing a memory with the same scope and name, and
// rewrites the scope's index. created is false when it replaced one.
func (s *Store) Write(m Memory) (created bool, err error) {
	if err := m.Validate(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir, path := s.dirs[m.Scope], s.Path(m.Scope, m.Name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, err
	}
	_, statErr := os.Stat(path)
	created = errors.Is(statErr, fs.ErrNotExist)
	if err := writeFile(path, m.file()); err != nil {
		return false, err
	}
	return created, s.writeIndex(m.Scope)
}

func (s *Store) Delete(scope Scope, name string) error {
	if _, err := s.Read(scope, name); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(s.Path(scope, name)); err != nil {
		return err
	}
	return s.writeIndex(scope)
}

// writeIndex rewrites INDEX.md for people reading the folder; loom itself
// builds the prompt from the files.
func (s *Store) writeIndex(scope Scope) error {
	mems, _ := s.list(scope)
	var b strings.Builder
	fmt.Fprintf(&b, "# Memory index (%s)\n\nWritten by loom after every change; edit the memory files, not this one.\n\n", scope)
	for _, m := range mems {
		b.WriteString(m.Line() + "\n")
	}
	return writeFile(filepath.Join(s.dirs[scope], IndexFile), []byte(b.String()))
}

// writeFile writes through a temporary file, so a crash never leaves half a
// memory behind.
func writeFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	_, err = tmp.Write(data)
	if err = errors.Join(err, tmp.Close()); err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
	}
	return err
}

// Change is one file a plan changed: Action is create, update or delete.
type Change struct {
	Path   string
	Action string
}

type snapshot struct {
	data    []byte
	existed bool
}

// Apply carries out a plan all together: when one step fails, every file it
// touched is put back as it was, and no change is reported.
func (s *Store) Apply(plan Plan) ([]Change, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	saved := map[string]snapshot{}
	var changes []Change
	err := s.apply(plan, saved, &changes)
	if err != nil {
		err = errors.Join(err, restore(saved))
		changes = nil
	}
	for _, scope := range Scopes {
		if _, statErr := os.Stat(s.dirs[scope]); statErr == nil {
			err = errors.Join(err, s.writeIndex(scope))
		}
	}
	return changes, err
}

func (s *Store) apply(plan Plan, saved map[string]snapshot, changes *[]Change) error {
	keep := func(path string) (snapshot, error) {
		if snap, ok := saved[path]; ok {
			return snap, nil
		}
		data, err := os.ReadFile(path) //nolint:gosec // G304: a validated name in loom's own memory folder
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return snapshot{}, err
		}
		saved[path] = snapshot{data: data, existed: err == nil}
		return saved[path], nil
	}
	for _, m := range plan.Write {
		if err := m.Validate(); err != nil {
			return err
		}
		path := s.Path(m.Scope, m.Name)
		snap, err := keep(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(s.dirs[m.Scope], 0o700); err != nil {
			return err
		}
		if err := writeFile(path, m.file()); err != nil {
			return err
		}
		action := ActionUpdate
		if !snap.existed {
			action = ActionCreate
		}
		*changes = append(*changes, Change{Path: path, Action: action})
	}
	for _, k := range plan.Delete {
		if err := checkKey(k.Scope, k.Name); err != nil {
			return err
		}
		path := s.Path(k.Scope, k.Name)
		if _, err := keep(path); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		*changes = append(*changes, Change{Path: path, Action: ActionDelete})
	}
	return nil
}

func restore(saved map[string]snapshot) error {
	var errs []error
	for path, snap := range saved {
		if snap.existed {
			errs = append(errs, writeFile(path, snap.data))
		} else if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
