// Package frontend is the API every UI drives loom through (docs/frontend.md).
package frontend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sync"

	"github.com/xzwache/loom/internal/agent"
	"github.com/xzwache/loom/internal/app"
	"github.com/xzwache/loom/internal/config"
	"github.com/xzwache/loom/internal/event"
	"github.com/xzwache/loom/internal/llm"
)

type UI interface {
	// Update is called one at a time, in order, and the agent waits for it:
	// it must not block. It may call Cancel, not State or Close.
	Update(u Update)
}

var (
	ErrBusy   = errors.New("frontend: a prompt is already running")
	ErrClosed = errors.New("frontend: the session is closed")
)

// EndReason is why a session ended, as SessionEnd hooks get it.
type EndReason string

const (
	EndExit  EndReason = "prompt_input_exit" // /exit, Ctrl+D
	EndClear EndReason = "clear"
	EndOther EndReason = "other" // the UI went away
)

// Session is what a UI drives, for many prompts. /clear swaps the
// conversation inside it for a new one.
type Session struct {
	cfg      config.Config
	opts     app.Options
	ui       UI
	log      *slog.Logger
	commands []Command

	// slot is held while a prompt or Close runs; only its holder touches
	// conversation and closed.
	slot         chan struct{}
	conversation *app.Conversation
	closed       bool

	runMu  sync.Mutex // guards cancel
	cancel context.CancelFunc

	mu    sync.Mutex // orders updates, guards state
	state state
}

// Open builds the conversation. Updates start during Open, so a UI sees
// startup too.
func Open(ctx context.Context, cfg config.Config, opts app.Options, ui UI) (*Session, error) {
	session := &Session{cfg: cfg, ui: ui, log: opts.Log, commands: builtin(), slot: make(chan struct{}, 1)}
	if session.log == nil {
		session.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	opts.Observe = session.observe
	session.opts = opts
	conv, err := app.Build(ctx, cfg, opts)
	if err != nil {
		return nil, err
	}
	session.conversation = conv
	return session, nil
}

// Prompt runs a slash command or one agent turn. TurnDone is the last update.
func (s *Session) Prompt(ctx context.Context, text string) (agent.Result, error) {
	select {
	case s.slot <- struct{}{}:
	default:
		return agent.Result{}, ErrBusy
	}
	defer func() { <-s.slot }()
	if s.closed {
		return agent.Result{}, ErrClosed
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.setCancel(cancel)
	defer s.setCancel(nil)

	var res agent.Result
	var err error
	if name, args, ok := parseCommand(text); ok {
		res, err = s.runCommand(ctx, name, args)
	} else {
		res, err = s.conversation.Run(ctx, text)
	}
	s.endTurn(res, err)
	return res, err
}

func (s *Session) Commands() []Command { return slices.Clone(s.commands) }

// Cancel stops the running prompt, if any; the session stays usable.
func (s *Session) Cancel() {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
}

func (s *Session) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.snapshot()
}

// Close cancels a running prompt, waits for it, then ends the session.
// Closing again does nothing.
func (s *Session) Close(ctx context.Context, reason EndReason) llm.Usage {
	s.Cancel()
	s.slot <- struct{}{}
	defer func() { <-s.slot }()
	return s.end(ctx, reason)
}

// end and restart need the slot held.
func (s *Session) end(ctx context.Context, reason EndReason) llm.Usage {
	if s.closed {
		return llm.Usage{}
	}
	s.closed = true
	used := s.conversation.End(context.WithoutCancel(ctx), string(reason))
	s.conversation.Close()
	return used
}

func (s *Session) restart(ctx context.Context, reason EndReason) (llm.Usage, error) {
	used := s.end(ctx, reason)
	opts := s.opts
	opts.Resume, opts.Latest = "", false
	conv, err := app.Build(ctx, s.cfg, opts)
	if err != nil {
		return used, fmt.Errorf("new session: %w", err)
	}
	s.conversation, s.closed = conv, false
	s.mu.Lock()
	s.state = state{}
	s.mu.Unlock()
	return used, nil
}

func (s *Session) setCancel(cancel context.CancelFunc) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	s.cancel = cancel
}

func (s *Session) observe(ctx context.Context, e event.Event) {
	body, ok := bodyOf(e)
	if !ok {
		return
	}
	from, _ := event.AgentFrom(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.send(from, body)
}

// notify sends an update from the frontend itself, not from an event.
func (s *Session) notify(body Body) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.send(event.Agent{}, body)
}

// send records body in the state and gives it to the UI. An update that does
// not fit the state is a loom bug: logged, still sent. s.mu must be held.
func (s *Session) send(from event.Agent, body Body) {
	if err := body.apply(&s.state, from); err != nil {
		s.log.Warn("frontend: update does not fit the state", "update", fmt.Sprintf("%T", body), "error", err)
	}
	s.ui.Update(Update{Agent: from, Body: body})
}

// endTurn finishes the tool calls the run left open, then sends TurnDone.
func (s *Session) endTurn(res agent.Result, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	status, why := StatusSkipped, "not run: the run ended"
	if res.Stop == agent.StopCancelled {
		status, why = StatusCancelled, "cancelled"
	}
	for _, t := range slices.Clone(s.state.tools) {
		s.send(t.Agent, ToolFinished{ID: t.ID, Status: status, Output: why})
	}
	s.send(event.Agent{}, TurnDone{Stop: res.Stop, Usage: res.Usage, Err: err})
}
