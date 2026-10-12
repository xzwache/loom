package permission

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
)

//go:embed floor_rules.json
var builtinFloorRules []byte

type FloorRules struct {
	BlockedCommands []BlockedCommand `json:"blocked_commands,omitempty"`
	// ProtectedPaths may not be deleted with rm -r.
	ProtectedPaths []string `json:"protected_paths,omitempty"`
	// DevicePrefixes may not be written with dd or a redirect.
	DevicePrefixes []string `json:"device_prefixes,omitempty"`
}

// BlockedCommand uses the same command syntax as a Bash rule.
type BlockedCommand struct {
	Command string `json:"command"`
	Reason  string `json:"reason"`
}

type floor struct {
	blocked   []blockedCommand
	protected map[string]bool
	devices   []string
}

type blockedCommand struct {
	pattern *regexp.Regexp
	reason  string
}

var forkBomb = regexp.MustCompile(`(\w+|:)\s*\(\s*\)\s*\{[^}]*(\w+|:)\s*\|\s*(\w+|:)\s*&`)

func newFloor(extra FloorRules) (*floor, error) {
	var rules FloorRules
	if err := json.Unmarshal(builtinFloorRules, &rules); err != nil {
		return nil, fmt.Errorf("built-in floor rules: %w", err)
	}
	f := &floor{protected: map[string]bool{}}
	for _, b := range slices.Concat(rules.BlockedCommands, extra.BlockedCommands) {
		if strings.TrimSpace(b.Command) == "" {
			return nil, errors.New("floor rule with an empty command")
		}
		reason := b.Reason
		if reason == "" {
			reason = "matches floor rule " + b.Command
		}
		f.blocked = append(f.blocked, blockedCommand{pattern: commandPattern(b.Command), reason: reason})
	}
	for _, p := range slices.Concat(rules.ProtectedPaths, extra.ProtectedPaths) {
		f.protected[p] = true
	}
	f.devices = slices.Concat(rules.DevicePrefixes, extra.DevicePrefixes)
	return f, nil
}

// check returns why src must never run, or "" when it may.
func (f *floor) check(src string, cmds []command) string {
	if forkBomb.MatchString(src) {
		return "fork bomb"
	}
	for _, c := range cmds {
		for _, t := range c.writes {
			if f.isDevice(t) {
				return "writes to a disk device"
			}
		}
		words := stripWrappers(stripPrivilege(c.words))
		if len(words) == 0 {
			continue
		}
		text := strings.Join(words, " ")
		for _, b := range f.blocked {
			if b.pattern.MatchString(text) {
				return b.reason
			}
		}
		name, args := words[0], words[1:]
		switch {
		case name == "rm" && recursive(args) && f.anyProtected(args):
			return "recursive delete of a protected directory"
		case name == "dd" && f.anyDevice(args):
			return "writes to a disk device"
		}
	}
	return ""
}

func stripPrivilege(words []string) []string {
	for len(words) > 0 {
		switch words[0] {
		case "sudo", "doas":
			words = skipOptions(words[1:], "-u", "-g", "-C", "-D", "-h", "-p", "-r", "-t", "-U")
		case "env":
			words = skipOptions(words[1:], "-u", "-C", "-S")
			for len(words) > 0 && strings.Contains(words[0], "=") {
				words = words[1:]
			}
		default:
			return words
		}
	}
	return words
}

func recursive(args []string) bool {
	for _, a := range args {
		if a == "--recursive" || (strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.ContainsAny(a, "rR")) {
			return true
		}
	}
	return false
}

func (f *floor) anyProtected(args []string) bool {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		if f.protected[a] || (strings.HasPrefix(a, "/") && f.protected[path.Clean(a)]) {
			return true
		}
	}
	return false
}

func (f *floor) anyDevice(args []string) bool {
	for _, a := range args {
		if target, ok := strings.CutPrefix(a, "of="); ok && f.isDevice(target) {
			return true
		}
	}
	return false
}

func (f *floor) isDevice(target string) bool {
	for _, p := range f.devices {
		if strings.HasPrefix(target, p) {
			return true
		}
	}
	return false
}
