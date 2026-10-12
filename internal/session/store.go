package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

type MemoryStore struct {
	mu       sync.Mutex
	sessions map[string]Session
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{sessions: map[string]Session{}} }

func (s *MemoryStore) Load(_ context.Context, id string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.sessions[id]
	if !ok {
		return Session{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return c, nil
}

func (s *MemoryStore) Save(_ context.Context, c Session) error {
	if !validID.MatchString(c.ID) {
		return fmt.Errorf("invalid session id %q", c.ID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c.History = c.History.clone()
	s.sessions[c.ID] = c
	return nil
}

func (s *MemoryStore) List(context.Context) ([]Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all := make([]Session, 0, len(s.sessions))
	for _, c := range s.sessions {
		all = append(all, c)
	}
	slices.SortFunc(all, newestFirst)
	return all, nil
}

// FileStore keeps each session in dir/<id>.json, readable only by the owner.
type FileStore struct {
	dir string
}

func NewFileStore(dir string) *FileStore { return &FileStore{dir: dir} }

func (s *FileStore) Load(_ context.Context, id string) (Session, error) {
	if !validID.MatchString(id) {
		return Session{}, fmt.Errorf("invalid session id %q", id)
	}
	return s.read(filepath.Join(s.dir, id+".json"))
}

// Save writes to a temporary file and renames it, so a crash never leaves a
// half-written session.
func (s *FileStore) Save(_ context.Context, c Session) error {
	if !validID.MatchString(c.ID) {
		return fmt.Errorf("invalid session id %q", c.ID)
	}
	data, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("save session: %w", err)
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("save session: %w", err)
	}
	tmp, err := os.CreateTemp(s.dir, c.ID+".*.tmp")
	if err != nil {
		return fmt.Errorf("save session: %w", err)
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(data)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("save session: %w", err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(s.dir, c.ID+".json")); err != nil {
		return fmt.Errorf("save session: %w", err)
	}
	return nil
}

func (s *FileStore) List(context.Context) ([]Session, error) {
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	var all []Session
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		c, err := s.read(filepath.Join(s.dir, e.Name()))
		if err != nil {
			return nil, err
		}
		all = append(all, c)
	}
	slices.SortFunc(all, newestFirst)
	return all, nil
}

func (s *FileStore) read(name string) (Session, error) {
	data, err := os.ReadFile(name) //nolint:gosec // G304: the name is built from a validated id
	if errors.Is(err, fs.ErrNotExist) {
		return Session{}, fmt.Errorf("%w: %s", ErrNotFound, strings.TrimSuffix(filepath.Base(name), ".json"))
	}
	if err != nil {
		return Session{}, fmt.Errorf("load session: %w", err)
	}
	var c Session
	if err := json.Unmarshal(data, &c); err != nil {
		return Session{}, fmt.Errorf("load session %s: %w", name, err)
	}
	return c, nil
}
