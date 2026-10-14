// Package permission decides whether a tool call may run, from allow, ask and
// deny rules in the style of Claude Code. It runs as a PreToolUse hook.
package permission

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xzwache/loom/internal/event"
	"github.com/xzwache/loom/internal/hook"
	"github.com/xzwache/loom/internal/tool"
)

type Mode string

const (
	ModeDefault     Mode = "default"
	ModeAcceptEdits Mode = "acceptEdits"
	ModePlan        Mode = "plan"
	ModeBypass      Mode = "bypassPermissions"
)

type Settings struct {
	Mode  Mode       `json:"mode"`
	Allow []string   `json:"allow"`
	Ask   []string   `json:"ask"`
	Deny  []string   `json:"deny"`
	Floor FloorRules `json:"floor"`
}

type Policy struct {
	mode    Mode
	allow   []Rule
	ask     []Rule
	deny    []Rule
	dir     string
	realDir string
	home    string
	floor   *floor
}

// New builds a policy for a workspace rooted at dir. home expands "~/" in
// path rules; when empty, those rules never match.
func New(s Settings, dir, home string) (*Policy, error) {
	p := &Policy{mode: s.Mode, dir: filepath.Clean(dir), home: home}
	switch p.mode {
	case "":
		p.mode = ModeDefault
	case ModeDefault, ModeAcceptEdits, ModePlan, ModeBypass:
	default:
		return nil, fmt.Errorf("unknown permission mode %q", s.Mode)
	}
	var errs []error
	for _, list := range []struct {
		src []string
		dst *[]Rule
	}{{s.Allow, &p.allow}, {s.Ask, &p.ask}, {s.Deny, &p.deny}} {
		for _, raw := range list.src {
			r, err := ParseRule(raw)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			*list.dst = append(*list.dst, r)
		}
	}
	floor, err := newFloor(s.Floor)
	if err != nil {
		errs = append(errs, err)
	}
	p.floor = floor
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	p.realDir = p.dir
	if real, err := filepath.EvalSymlinks(p.dir); err == nil {
		p.realDir = real
	}
	return p, nil
}

func (s Settings) Validate() error {
	_, err := New(s, ".", "")
	return err
}

func (p *Policy) Hook(_ context.Context, e event.PreToolUse) (hook.Outcome, error) {
	d, reason := p.Check(e.ToolName, e.ToolInput)
	return hook.Outcome{Decision: d, Reason: reason}, nil
}

// request is one thing to decide: a file call, a tool call, or one command
// of a shell command line.
type request struct {
	tool string
	// match reports whether a rule's specifier matches. all is set for allow
	// rules, which must match every form of the target (a symlink and the
	// file it points to); deny and ask rules need to match only one.
	match func(r Rule, all bool) bool
	// free requests run without a rule: reads and safe commands.
	free bool
	edit bool
}

// Check decides a tool call: deny rules first, then the mode, then ask and
// allow rules, then the defaults (reads run, edits run in acceptEdits mode,
// everything else asks).
func (p *Policy) Check(name string, input json.RawMessage) (hook.Decision, string) {
	name = strings.ToLower(name)
	switch name {
	case tool.Bash:
		return p.checkBash(input)
	case tool.Read, tool.Edit, tool.Write:
		var in struct {
			Path string `json:"file_path"`
		}
		if err := json.Unmarshal(input, &in); err != nil {
			return hook.Deny, "cannot read the tool input: " + err.Error()
		}
		targets := p.resolve(in.Path)
		return p.decide(request{
			tool: name,
			match: func(r Rule, all bool) bool {
				n := 0
				for _, t := range targets {
					if r.matchPath(t, p.home) {
						n++
					}
				}
				return n == len(targets) || (!all && n > 0)
			},
			free: name == tool.Read,
			edit: name != tool.Read,
		})
	default:
		return p.decide(request{tool: name, match: func(r Rule, _ bool) bool { return r.spec == "" }, free: freeTools[name]})
	}
}

func (p *Policy) checkBash(input json.RawMessage) (hook.Decision, string) {
	var in struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return hook.Deny, "cannot read the tool input: " + err.Error()
	}
	cmds, err := parseCommands(in.Command)
	if err != nil {
		return p.decide(request{tool: tool.Bash, match: func(r Rule, _ bool) bool { return r.spec == "" }})
	}
	if reason := p.floor.check(in.Command, cmds); reason != "" {
		return hook.Deny, "never allowed: " + reason
	}

	decision, reason := hook.Allow, ""
	for _, c := range cmds {
		text := c.String()
		d, why := p.decide(request{
			tool:  tool.Bash,
			match: func(r Rule, _ bool) bool { return r.matchCommand(text) },
			free:  c.safe(),
		})
		if rank(d) > rank(decision) {
			decision, reason = d, why
			if reason != "" && len(cmds) > 1 {
				reason = fmt.Sprintf("%s (in %q)", reason, text)
			}
		}
	}
	return decision, reason
}

func (p *Policy) decide(rq request) (hook.Decision, string) {
	for _, r := range p.deny {
		if r.covers(rq.tool) && rq.match(r, false) {
			return hook.Deny, "denied by rule " + r.raw
		}
	}
	if p.mode == ModePlan && !rq.free {
		return hook.Deny, "plan mode allows only reading"
	}
	if p.mode == ModeBypass {
		return hook.Allow, ""
	}
	for _, r := range p.ask {
		if r.covers(rq.tool) && rq.match(r, false) {
			return hook.Ask, "rule " + r.raw + " asks for approval"
		}
	}
	for _, r := range p.allow {
		if r.covers(rq.tool) && rq.match(r, true) {
			return hook.Allow, ""
		}
	}
	if rq.free || (rq.edit && p.mode == ModeAcceptEdits) {
		return hook.Allow, ""
	}
	return hook.Ask, "no rule allows this " + rq.tool + " call"
}

func rank(d hook.Decision) int {
	switch d {
	case hook.Deny:
		return 2
	case hook.Ask:
		return 1
	default:
		return 0
	}
}

// freeTools only read or only change loom's own state, so they run without a
// rule. A subagent's own tool calls are checked like any other.
var freeTools = map[string]bool{tool.Agent: true, tool.Glob: true, tool.Todo: true}

type filePath struct {
	abs     string
	rel     string
	outside bool
}

// resolve returns the path as given and, when it differs, the path with
// symlinks resolved, both relative to the workspace.
func (p *Policy) resolve(name string) []filePath {
	abs := name
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(p.dir, abs)
	}
	abs = filepath.Clean(abs)
	paths := []filePath{newFilePath(abs, p.dir)}
	if real := realPath(abs); real != abs {
		paths = append(paths, newFilePath(real, p.realDir))
	}
	return paths
}

func newFilePath(abs, dir string) filePath {
	rel, err := filepath.Rel(dir, abs)
	outside := err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
	return filePath{abs: filepath.ToSlash(abs), rel: filepath.ToSlash(rel), outside: outside}
}

// realPath resolves symlinks in abs. For a file that does not exist yet it
// resolves the directory, and for a dangling symlink the link itself, since
// writing through it creates its target.
func realPath(abs string) string {
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	dir := filepath.Dir(abs)
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	target, err := os.Readlink(abs)
	if err != nil {
		return filepath.Join(dir, filepath.Base(abs))
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(dir, target)
	}
	return filepath.Clean(target)
}
