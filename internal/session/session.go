// Package session keeps sessions between runs, so a run can be resumed with
// its full history.
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"regexp"
	"time"

	"github.com/xzwache/loom/internal/llm"
)

var ErrNotFound = errors.New("session not found")

type Session struct {
	ID      string    `json:"id"`
	Cwd     string    `json:"cwd"`
	Model   string    `json:"model"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
	Usage   llm.Usage `json:"usage"`
	History History   `json:"history"`
}

type Store interface {
	Load(ctx context.Context, id string) (Session, error)
	Save(ctx context.Context, s Session) error
	// List returns every session, most recently updated first.
	List(ctx context.Context) ([]Session, error)
}

var validID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func New(now time.Time, cwd, model string) Session {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	id := now.UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b)
	return Session{ID: id, Cwd: cwd, Model: model, Created: now, Updated: now}
}

// Latest returns the most recently updated session in store.
func Latest(ctx context.Context, store Store) (Session, error) {
	all, err := store.List(ctx)
	if err != nil {
		return Session{}, err
	}
	if len(all) == 0 {
		return Session{}, ErrNotFound
	}
	return all[0], nil
}

func newestFirst(a, b Session) int { return b.Updated.Compare(a.Updated) }
