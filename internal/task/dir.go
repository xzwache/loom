package task

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// A workspace's tasks live in lists, one folder each: session-<session id>
// for a session's own list, or a name the user picks for a shared one.
const sessionPrefix = "session-"

var validListName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ValidateListName checks a name the user picks for a shared list.
func ValidateListName(name string) error {
	switch {
	case !validListName.MatchString(name):
		return fmt.Errorf("list name %q: use 1-64 lowercase letters, digits, - and _", name)
	case strings.HasPrefix(name, sessionPrefix):
		return fmt.Errorf("list name %q: %s is kept for session lists", name, sessionPrefix)
	}
	return nil
}

// SessionList is the name of a session's own list.
func SessionList(sessionID string) string { return sessionPrefix + sessionID }

// PruneSessionLists deletes the session lists in root that have not changed
// for maxAge, except keep, and returns the names it deleted. Named lists are
// never deleted: the user made them to keep.
func PruneSessionLists(root string, maxAge time.Duration, now time.Time, keep string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var deleted []string
	var errs []error
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || !strings.HasPrefix(name, sessionPrefix) || name == keep {
			continue
		}
		info, err := e.Info()
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if now.Sub(info.ModTime()) < maxAge {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, name)); err != nil {
			errs = append(errs, err)
			continue
		}
		deleted = append(deleted, name)
	}
	return deleted, errors.Join(errs...)
}
