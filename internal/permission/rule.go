package permission

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/xzwache/loom/internal/tool"
)

// Rule is one entry of an allow, ask or deny list: "Tool" or "Tool(specifier)".
type Rule struct {
	raw     string
	tool    string
	spec    string
	command *regexp.Regexp
}

// Tools whose rules take a specifier, and the kind of specifier.
var specKinds = map[string]string{
	tool.Bash:  "command",
	tool.Read:  "path",
	tool.Edit:  "path",
	tool.Write: "path",
}

func ParseRule(s string) (Rule, error) {
	r := Rule{raw: s, tool: strings.ToLower(strings.TrimSpace(s))}
	if open := strings.IndexByte(s, '('); open >= 0 {
		if !strings.HasSuffix(s, ")") {
			return Rule{}, fmt.Errorf("rule %q: missing closing parenthesis", s)
		}
		r.tool = strings.ToLower(strings.TrimSpace(s[:open]))
		r.spec = s[open+1 : len(s)-1]
		if r.spec == "" {
			return Rule{}, fmt.Errorf("rule %q: empty specifier", s)
		}
	}
	if r.tool == "" {
		return Rule{}, fmt.Errorf("rule %q: no tool name", s)
	}
	if r.spec == "" {
		return r, nil
	}
	switch specKinds[r.tool] {
	case "command":
		r.command = commandPattern(r.spec)
	case "path":
		if !doublestar.ValidatePattern(strings.TrimPrefix(r.spec, "/")) {
			return Rule{}, fmt.Errorf("rule %q: invalid path pattern", s)
		}
	default:
		return Rule{}, fmt.Errorf("rule %q: %s rules do not take a specifier", s, r.tool)
	}
	return r, nil
}

func (r Rule) String() string { return r.raw }

// commandPattern turns a command specifier into a regexp: * matches any text,
// and a trailing " *" (or the legacy ":*") also matches the bare command.
func commandPattern(spec string) *regexp.Regexp {
	spec = strings.TrimSpace(spec)
	optionalArgs := false
	for _, suffix := range []string{" *", ":*"} {
		if s, ok := strings.CutSuffix(spec, suffix); ok {
			spec, optionalArgs = s, true
			break
		}
	}
	parts := strings.Split(spec, "*")
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	expr := strings.Join(parts, ".*")
	if optionalArgs {
		expr += "( .*)?"
	}
	return regexp.MustCompile(`^` + expr + `$`)
}

// covers reports whether a rule for r.tool applies to the named tool: Edit
// rules also cover write, as in Claude Code, and an MCP server rule
// ("mcp__github") covers each of its tools.
func (r Rule) covers(name string) bool {
	return tool.Covers(r.tool, name) || (r.tool == tool.Edit && name == tool.Write)
}

func (r Rule) matchCommand(cmd string) bool {
	return r.command == nil || r.command.MatchString(cmd)
}

// matchPath matches a gitignore-style specifier: "//abs" is an absolute path,
// "~/x" is under home, anything else is relative to the workspace, and a
// pattern without a slash (or a leading "/" or "./") matches a name at any
// depth. A pattern also matches
// everything below a matching directory.
func (r Rule) matchPath(p filePath, home string) bool {
	if r.spec == "" {
		return true
	}
	var pattern, target string
	switch {
	case strings.HasPrefix(r.spec, "//"):
		pattern, target = r.spec[1:], p.abs
	case strings.HasPrefix(r.spec, "~/"):
		if home == "" {
			return false
		}
		pattern, target = path.Join(home, r.spec[2:]), p.abs
	default:
		if p.outside {
			return false
		}
		pattern = strings.TrimPrefix(strings.TrimPrefix(r.spec, "./"), "/")
		anchored := pattern != r.spec
		if !anchored && !strings.Contains(pattern, "/") {
			pattern = "**/" + pattern
		}
		target = p.rel
	}
	return doublestar.MatchUnvalidated(pattern, target) || doublestar.MatchUnvalidated(pattern+"/**", target)
}
