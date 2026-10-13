// Package command runs hook scripts configured: the
// event goes to the script as JSON on stdin; exit code 2 blocks, and on exit
// code 0 the JSON on stdout decides the outcome. Any other failure is logged
// and ignored, as in Claude Code.
package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/xzwache/loom/internal/event"
	"github.com/xzwache/loom/internal/hook"
)

const defaultTimeout = 60 * time.Second

// Settings maps an event name to its matchers, as in Claude Code's "hooks".
type Settings map[event.Name][]Matcher

// Matcher is a regexp on the tool name for tool events; empty or "*" matches
// every tool. It is ignored for other events.
type Matcher struct {
	Matcher string    `json:"matcher,omitempty"`
	Hooks   []Handler `json:"hooks"`
}

type Handler struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	// Timeout is in seconds.
	Timeout int `json:"timeout,omitempty"`
}

// Env is what every script receives besides the event.
type Env struct {
	SessionID      string
	Dir            string
	PermissionMode string
	Log            *slog.Logger
}

// Variables that scripts never see.
var hiddenEnv = []string{"ANTHROPIC_API_KEY"}

func (s Settings) Validate() error {
	var errs []error
	for name, matchers := range s {
		if !event.Known(name) {
			errs = append(errs, fmt.Errorf("hooks: unknown event %q", name))
		}
		for _, m := range matchers {
			if _, err := compile(m.Matcher); err != nil {
				errs = append(errs, fmt.Errorf("hooks: %s matcher %q: %w", name, m.Matcher, err))
			}
			for _, h := range m.Hooks {
				switch {
				case h.Type != "command":
					errs = append(errs, fmt.Errorf("hooks: %s: unsupported hook type %q", name, h.Type))
				case strings.TrimSpace(h.Command) == "":
					errs = append(errs, fmt.Errorf("hooks: %s: empty command", name))
				case h.Timeout < 0:
					errs = append(errs, fmt.Errorf("hooks: %s: negative timeout", name))
				}
			}
		}
	}
	return errors.Join(errs...)
}

func Register(c *hook.Pipeline, s Settings, env Env) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if env.Log == nil {
		env.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	for _, name := range slices.Sorted(maps.Keys(s)) {
		for _, m := range s[name] {
			re, _ := compile(m.Matcher)
			for _, h := range m.Hooks {
				c.OnEvent(name, &script{handler: h, match: re, env: env})
			}
		}
	}
	return nil
}

func compile(matcher string) (*regexp.Regexp, error) {
	if matcher == "" || matcher == "*" {
		return nil, nil
	}
	return regexp.Compile(`(?i)^(?:` + matcher + `)$`)
}

type script struct {
	handler Handler
	match   *regexp.Regexp
	env     Env
}

func (s *script) Handle(ctx context.Context, e event.Event) (hook.Outcome, error) {
	if call, ok := event.Tool(e); ok && s.match != nil && !s.match.MatchString(call.ToolName) {
		return hook.Outcome{}, nil
	}
	input, err := s.input(ctx, e)
	if err != nil {
		return hook.Outcome{}, err
	}
	log := s.env.Log.With("event", e.Name(), "command", s.handler.Command)

	stdout, stderr, code, err := s.run(ctx, input)
	switch {
	case err != nil:
		log.Warn("hook script failed", "error", err)
		return hook.Outcome{}, nil
	case code == 2:
		return blocked(e, strings.TrimSpace(stderr), s.handler.Command), nil
	case code != 0:
		log.Warn("hook script failed", "exit_code", code, "stderr", strings.TrimSpace(stderr))
		return hook.Outcome{}, nil
	}
	out, err := parse(e, stdout)
	if err != nil {
		log.Warn("hook script printed invalid JSON", "error", err)
		return hook.Outcome{}, nil
	}
	if out.message != "" {
		log.Info("hook message", "message", out.message)
	}
	return out.Outcome, nil
}

// input is the event JSON plus the fields Claude Code sends with every event.
// Built-in tool names are capitalized as in Claude Code, so scripts written
// for it match.
func (s *script) input(ctx context.Context, e event.Event) ([]byte, error) {
	data, err := json.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", e.Name(), err)
	}
	fields := map[string]any{}
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, fmt.Errorf("encode %s: %w", e.Name(), err)
	}
	fields["hook_event_name"] = e.Name()
	fields["session_id"] = s.env.SessionID
	fields["cwd"] = s.env.Dir
	fields["permission_mode"] = s.env.PermissionMode
	if sub, ok := event.AgentFrom(ctx); ok {
		fields["agent_id"], fields["agent_type"] = sub.ID, sub.Type
	}
	if name, ok := fields["tool_name"].(string); ok {
		fields["tool_name"] = claudeToolName(name)
	}
	return json.Marshal(fields)
}

func claudeToolName(name string) string {
	switch name {
	case "agent", "bash", "read", "write", "edit", "glob":
		return strings.ToUpper(name[:1]) + name[1:]
	default:
		return name
	}
}

func (s *script) run(ctx context.Context, input []byte) (stdout, stderr string, code int, err error) {
	timeout := defaultTimeout
	if s.handler.Timeout > 0 {
		timeout = time.Duration(s.handler.Timeout) * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", s.handler.Command) //nolint:gosec // G204: hook commands come from the user's config on purpose
	cmd.Dir = s.env.Dir
	cmd.Env = append(scriptEnv(), "LOOM_PROJECT_DIR="+s.env.Dir, "CLAUDE_PROJECT_DIR="+s.env.Dir)
	cmd.Stdin = bytes.NewReader(input)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	cmd.WaitDelay = time.Second

	err = cmd.Run()
	if ctx.Err() != nil {
		return "", "", 0, fmt.Errorf("timed out after %s", timeout)
	}
	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		return out.String(), errOut.String(), exit.ExitCode(), nil
	}
	if err != nil {
		return "", "", 0, err
	}
	return out.String(), errOut.String(), 0, nil
}

func scriptEnv() []string {
	return slices.DeleteFunc(os.Environ(), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return slices.Contains(hiddenEnv, name)
	})
}

// blocked maps exit code 2: a denial for tool permission events, a block
// (with stderr shown to the model) for the rest.
func blocked(e event.Event, stderr, command string) hook.Outcome {
	reason := stderr
	if reason == "" {
		reason = "blocked by hook " + command
	}
	switch e.(type) {
	case event.PreToolUse, event.PermissionRequest:
		return hook.Outcome{Decision: hook.Deny, Reason: reason}
	default:
		return hook.Outcome{Block: true, Reason: reason}
	}
}

type output struct {
	Continue           *bool  `json:"continue"`
	StopReason         string `json:"stopReason"`
	SystemMessage      string `json:"systemMessage"`
	Decision           string `json:"decision"`
	Reason             string `json:"reason"`
	HookSpecificOutput *struct {
		PermissionDecision       string          `json:"permissionDecision"`
		PermissionDecisionReason string          `json:"permissionDecisionReason"`
		UpdatedInput             json.RawMessage `json:"updatedInput"`
		AdditionalContext        string          `json:"additionalContext"`
		UpdatedToolOutput        *string         `json:"updatedToolOutput"`
		Decision                 *struct {
			Behavior string `json:"behavior"`
			Message  string `json:"message"`
		} `json:"decision"`
	} `json:"hookSpecificOutput"`
}

type parsed struct {
	hook.Outcome
	message string
}

// parse reads stdout of a script that exited 0. Plain text is context for the
// model on UserPromptSubmit and SessionStart, and ignored elsewhere.
func parse(e event.Event, stdout string) (parsed, error) {
	text := strings.TrimSpace(stdout)
	if text == "" {
		return parsed{}, nil
	}
	if !strings.HasPrefix(text, "{") {
		switch e.(type) {
		case event.UserPromptSubmit, event.SessionStart:
			return parsed{Outcome: hook.Outcome{AdditionalContext: text}}, nil
		default:
			return parsed{}, nil
		}
	}
	var o output
	if err := json.Unmarshal([]byte(text), &o); err != nil {
		return parsed{}, err
	}

	p := parsed{message: o.SystemMessage}
	var reasons []string
	if o.Continue != nil && !*o.Continue {
		p.Halt = true
		reasons = append(reasons, o.StopReason)
	}
	if o.Decision == "block" {
		p.Block = true
		reasons = append(reasons, o.Reason)
	}
	if h := o.HookSpecificOutput; h != nil {
		p.Decision = decision(h.PermissionDecision)
		reasons = append(reasons, h.PermissionDecisionReason)
		if h.Decision != nil {
			p.Decision = decision(h.Decision.Behavior)
			reasons = append(reasons, h.Decision.Message)
		}
		p.UpdatedInput = h.UpdatedInput
		p.AdditionalContext = h.AdditionalContext
		p.UpdatedOutput = h.UpdatedToolOutput
	}
	p.Reason = strings.Join(slices.DeleteFunc(reasons, func(r string) bool { return r == "" }), "; ")
	return p, nil
}

// decision maps Claude Code's values; "defer" and unknown values decide nothing.
func decision(s string) hook.Decision {
	switch s {
	case "allow":
		return hook.Allow
	case "ask":
		return hook.Ask
	case "deny":
		return hook.Deny
	default:
		return ""
	}
}
