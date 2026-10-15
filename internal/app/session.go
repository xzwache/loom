package app

import (
	"context"
	"errors"
	"time"

	"github.com/xzwache/loom/internal/agent"
	"github.com/xzwache/loom/internal/session"
)

// How a session started, as SessionStart reports it.
const (
	sourceStartup = "startup"
	sourceResume  = "resume"
)

// findSession returns the session to run in and how it started.
func findSession(ctx context.Context, store session.Store, opts Options, cwd, model string) (session.Session, string, error) {
	switch {
	case opts.Resume != "":
		s, err := store.Load(ctx, opts.Resume)
		return s, sourceResume, err
	case opts.Latest:
		s, err := session.Latest(ctx, store)
		if errors.Is(err, session.ErrNotFound) {
			return s, "", errors.New("no session to continue")
		}
		return s, sourceResume, err
	default:
		return session.New(time.Now(), cwd, model), sourceStartup, nil
	}
}

// saveSession stores the history Run returned, which is always valid to send
// again, even after an error or a budget stop.
func saveSession(ctx context.Context, store session.Store, sess *session.Session, res agent.Result) error {
	if len(res.Messages) == 0 {
		return nil
	}
	history, err := session.NewHistory(res.Messages)
	if err != nil {
		return err
	}
	sess.History, sess.Usage, sess.Updated = history, sess.Usage.Add(res.Usage), time.Now()
	return store.Save(ctx, *sess)
}
