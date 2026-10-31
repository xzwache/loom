// Package task keeps a graph of tasks that outlives a session: each task can
// wait on others (blocked_by) and records the agent working on it (owner).
// One JSON file per task, under loom's state folder.
package task

import (
	"cmp"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

type Status string

const (
	Pending    Status = "pending"
	InProgress Status = "in_progress"
	Completed  Status = "completed"
)

const (
	MaxSubject     = 200
	MaxDescription = 4000
)

var validID = regexp.MustCompile(`^task_[0-9a-f]{8}$`)

type Task struct {
	ID          string    `json:"id"`
	Subject     string    `json:"subject"`
	Description string    `json:"description,omitempty"`
	Status      Status    `json:"status"`
	Owner       string    `json:"owner,omitempty"`
	BlockedBy   []string  `json:"blocked_by"`
	Created     time.Time `json:"created"`
}

func (t Task) validate() error {
	if !validID.MatchString(t.ID) {
		return fmt.Errorf("invalid task id %q", t.ID)
	}
	if err := validateSubject(t.Subject, t.Description); err != nil {
		return err
	}
	switch t.Status {
	case Pending, InProgress, Completed:
	default:
		return fmt.Errorf("unknown status %q", t.Status)
	}
	// A completed task keeps the owner that finished it.
	if t.Status == InProgress && t.Owner == "" || t.Status == Pending && t.Owner != "" {
		return fmt.Errorf("status %s with owner %q", t.Status, t.Owner)
	}
	for _, id := range t.BlockedBy {
		if !validID.MatchString(id) {
			return fmt.Errorf("invalid blocked_by id %q", id)
		}
	}
	return nil
}

func validateSubject(subject, description string) error {
	switch {
	case strings.TrimSpace(subject) == "":
		return errors.New("subject is empty")
	case strings.Contains(subject, "\n"):
		return errors.New("subject must be one line")
	case len(subject) > MaxSubject:
		return fmt.Errorf("subject is longer than %d characters", MaxSubject)
	case len(description) > MaxDescription:
		return fmt.Errorf("description is longer than %d characters", MaxDescription)
	}
	return nil
}

func validateID(id string) error {
	if !validID.MatchString(id) {
		return fmt.Errorf("invalid task id %q", id)
	}
	return nil
}

// Store reads and writes the tasks of one workspace. It is safe for
// concurrent use, as parallel subagents claim tasks at the same time. It does
// not lock against a second loom process.
type Store struct {
	mu  sync.Mutex
	dir string
	now func() time.Time
}

func NewStore(dir string) *Store {
	return &Store{dir: dir, now: time.Now}
}

func (s *Store) Dir() string { return s.dir }

func (s *Store) path(id string) string { return filepath.Join(s.dir, id+".json") }

// Edit is a change to a pending task; nil fields stay as they are.
type Edit struct {
	Subject      *string
	Description  *string
	AddBlockedBy []string
}

func (s *Store) Create(subject, description string, blockedBy []string) (Task, error) {
	if err := validateSubject(subject, description); err != nil {
		return Task{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.loadAll()
	if err != nil {
		return Task{}, err
	}
	t := Task{Subject: strings.TrimSpace(subject), Description: description, Status: Pending, BlockedBy: []string{}, Created: s.now().UTC()}
	for t.ID == "" || all[t.ID].ID != "" {
		t.ID = newID()
	}
	if t.BlockedBy, err = addBlockers(all, t.ID, nil, blockedBy); err != nil {
		return Task{}, err
	}
	return t, s.save(t)
}

func (s *Store) Get(id string) (Task, error) {
	if err := validateID(id); err != nil {
		return Task{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load(id)
}

// List returns every readable task, oldest first, and an error for each file
// that is not.
func (s *Store) List() ([]Task, []error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.list()
}

func (s *Store) Edit(id string, e Edit) (Task, error) {
	if err := validateID(id); err != nil {
		return Task{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.loadAll()
	if err != nil {
		return Task{}, err
	}
	t, ok := all[id]
	if !ok {
		return Task{}, fmt.Errorf("task %s not found", id)
	}
	if t.Status != Pending {
		return Task{}, fmt.Errorf("task %s is %s; only pending tasks can be changed", id, t.Status)
	}
	if e.Subject != nil {
		t.Subject = strings.TrimSpace(*e.Subject)
	}
	if e.Description != nil {
		t.Description = *e.Description
	}
	if err := validateSubject(t.Subject, t.Description); err != nil {
		return Task{}, err
	}
	if t.BlockedBy, err = addBlockers(all, id, t.BlockedBy, e.AddBlockedBy); err != nil {
		return Task{}, err
	}
	return t, s.save(t)
}

// Claim starts a pending task whose blockers are all completed.
func (s *Store) Claim(id, owner string) (Task, error) {
	if err := validateID(id); err != nil {
		return Task{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.loadAll()
	if err != nil {
		return Task{}, err
	}
	t, ok := all[id]
	switch {
	case !ok:
		return Task{}, fmt.Errorf("task %s not found", id)
	case t.Status != Pending:
		return Task{}, fmt.Errorf("task %s is %s (owner %s); only pending tasks can be claimed", id, t.Status, cmp.Or(t.Owner, "none"))
	}
	if waiting := waitingOn(all, t); len(waiting) > 0 {
		return Task{}, fmt.Errorf("task %s is blocked by %s", id, strings.Join(waiting, ", "))
	}
	t.Status, t.Owner = InProgress, owner
	return t, s.save(t)
}

// Delete removes a task that is not in progress and that no unfinished task
// waits on, so deleting never leaves a task blocked for good.
func (s *Store) Delete(id string) (Task, error) {
	if err := validateID(id); err != nil {
		return Task{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.loadAll()
	if err != nil {
		return Task{}, err
	}
	t, ok := all[id]
	switch {
	case !ok:
		return Task{}, fmt.Errorf("task %s not found", id)
	case t.Status == InProgress:
		return Task{}, fmt.Errorf("task %s is in_progress (owner %s); complete or release it first", id, t.Owner)
	}
	var waiting []string
	for _, c := range sorted(all) {
		if c.Status != Completed && slices.Contains(c.BlockedBy, id) {
			waiting = append(waiting, c.ID)
		}
	}
	if len(waiting) > 0 {
		return Task{}, fmt.Errorf("task %s is a blocker of %s; delete those first", id, strings.Join(waiting, ", "))
	}
	return t, os.Remove(s.path(id))
}

// Complete finishes a task in progress. Only its owner may, or anyone with
// override (the main agent, which may finish a task a subagent left). It
// returns the tasks this made ready.
func (s *Store) Complete(id, by string, override bool) (Task, []Task, error) {
	if err := validateID(id); err != nil {
		return Task{}, nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.loadAll()
	if err != nil {
		return Task{}, nil, err
	}
	t, err := ownedInProgress(all, id, by, override)
	if err != nil {
		return Task{}, nil, err
	}
	ready := func(c Task) bool { return c.Status == Pending && len(c.BlockedBy) > 0 && len(waitingOn(all, c)) == 0 }
	before := map[string]bool{}
	for _, c := range all {
		before[c.ID] = ready(c)
	}
	t.Status = Completed
	all[id] = t
	if err := s.save(t); err != nil {
		return Task{}, nil, err
	}
	var unblocked []Task
	for _, c := range sorted(all) {
		if ready(c) && !before[c.ID] {
			unblocked = append(unblocked, c)
		}
	}
	return t, unblocked, nil
}

// Release puts a task in progress back to pending, under the same rule as
// Complete.
func (s *Store) Release(id, by string, override bool) (Task, error) {
	if err := validateID(id); err != nil {
		return Task{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.loadAll()
	if err != nil {
		return Task{}, err
	}
	t, err := ownedInProgress(all, id, by, override)
	if err != nil {
		return Task{}, err
	}
	t.Status, t.Owner = Pending, ""
	return t, s.save(t)
}

func ownedInProgress(all map[string]Task, id, by string, override bool) (Task, error) {
	t, ok := all[id]
	switch {
	case !ok:
		return Task{}, fmt.Errorf("task %s not found", id)
	case t.Status != InProgress:
		return Task{}, fmt.Errorf("task %s is %s, not in_progress", id, t.Status)
	case t.Owner != by && !override:
		return Task{}, fmt.Errorf("task %s is owned by %s, not %s", id, t.Owner, by)
	}
	return t, nil
}

func waitingOn(all map[string]Task, t Task) []string {
	var waiting []string
	for _, id := range t.BlockedBy {
		switch b, ok := all[id]; {
		case !ok:
			waiting = append(waiting, id+" (missing)")
		case b.Status != Completed:
			waiting = append(waiting, id+" ("+string(b.Status)+")")
		}
	}
	return waiting
}

// addBlockers adds add to current for task id, after checking that every
// blocker exists, is not id itself and does not wait on id.
func addBlockers(all map[string]Task, id string, current, add []string) ([]string, error) {
	out := slices.Clone(current)
	if out == nil {
		out = []string{}
	}
	for _, b := range add {
		switch _, ok := all[b]; {
		case b == id:
			return nil, fmt.Errorf("task %s cannot block itself", id)
		case !ok:
			return nil, fmt.Errorf("blocker %s not found", b)
		case slices.Contains(out, b):
			continue
		case reaches(all, b, id):
			return nil, fmt.Errorf("%s already waits on %s; adding it as a blocker would make a cycle", b, id)
		}
		out = append(out, b)
	}
	return out, nil
}

// reaches reports whether from waits on target, directly or through others.
func reaches(all map[string]Task, from, target string) bool {
	seen := map[string]bool{}
	stack := []string{from}
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if id == target {
			return true
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		stack = append(stack, all[id].BlockedBy...)
	}
	return false
}

func (s *Store) load(id string) (Task, error) {
	data, err := os.ReadFile(s.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return Task{}, fmt.Errorf("task %s not found", id)
	}
	if err != nil {
		return Task{}, err
	}
	return parse(id, data)
}

func parse(id string, data []byte) (Task, error) {
	var t Task
	if err := json.Unmarshal(data, &t); err != nil {
		return Task{}, fmt.Errorf("task %s: %w", id, err)
	}
	if t.ID != id {
		return Task{}, fmt.Errorf("task file %s holds id %q", id, t.ID)
	}
	if err := t.validate(); err != nil {
		return Task{}, fmt.Errorf("task %s: %w", id, err)
	}
	return t, nil
}

func (s *Store) list() ([]Task, []error) {
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, []error{err}
	}
	var tasks []Task
	var problems []error
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		t, err := s.load(id)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		tasks = append(tasks, t)
	}
	sortByCreated(tasks)
	return tasks, problems
}

// loadAll is every task by id. A change needs the whole graph, so an
// unreadable file stops it: it might be a blocker or close a cycle.
func (s *Store) loadAll() (map[string]Task, error) {
	tasks, problems := s.list()
	if len(problems) > 0 {
		return nil, fmt.Errorf("unreadable tasks, fix or remove them in %s: %w", s.dir, errors.Join(problems...))
	}
	return byID(tasks), nil
}

func sorted(all map[string]Task) []Task {
	tasks := slices.Collect(maps.Values(all))
	sortByCreated(tasks)
	return tasks
}

func sortByCreated(tasks []Task) {
	slices.SortFunc(tasks, func(a, b Task) int {
		return cmp.Or(a.Created.Compare(b.Created), strings.Compare(a.ID, b.ID))
	})
}

func (s *Store) save(t Task) error {
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	return writeFile(s.path(t.ID), append(data, '\n'))
}

// writeFile writes through a temporary file, so a crash never leaves half a
// task behind.
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

func newID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "task_" + hex.EncodeToString(b)
}
