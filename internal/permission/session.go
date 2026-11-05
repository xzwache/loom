package permission

import (
	"encoding/json"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/xzwache/loom/internal/hook"
	"github.com/xzwache/loom/internal/tool"
)

// SessionRules are the allow rules that would let this call run without
// asking again, for "allow always":
//   - bash: each command that asks, by its first word and subcommand
//     (Bash(go build *));
//   - read, edit, write: the file's directory (Edit(/internal/llm/**)), or
//     the file itself at the workspace root;
//   - other tools: the tool.
//
// Nil when no allow rule can make the call run: it is denied, an ask rule or
// the mode asks, or a part of it cannot be named by a rule.
func (p *Policy) SessionRules(name string, input json.RawMessage) []string {
	var found []Rule
	switch g := p.guard(name).(type) {
	case bashGuard:
		found = g.sessionRules(input)
	case fileGuard:
		found = g.sessionRules(input)
	case plainGuard:
		found = g.sessionRules()
	}
	var out []string
	for _, r := range found {
		if !slices.Contains(out, r.raw) {
			out = append(out, r.raw)
		}
	}
	return out
}

// AllowForSession adds allow rules until the policy is dropped.
func (p *Policy) AllowForSession(raw []string) error {
	var parsed rules
	for _, s := range raw {
		r, err := ParseRule(s)
		if err != nil {
			return err
		}
		parsed = append(parsed, r)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.session = append(p.session, parsed...)
	return nil
}

func (g bashGuard) sessionRules(input json.RawMessage) []Rule {
	in, err := decodeInput[struct {
		Command string `json:"command"`
	}](input)
	if err != nil {
		return nil
	}
	cmds, err := parseCommands(in.Command)
	if err != nil || g.floor.check(in.Command, cmds) != "" {
		return nil
	}
	var found []Rule
	for _, cmd := range cmds {
		t := commandTarget{cmd.String()}
		v := g.policy.evaluate(tool.Bash, cmd.access(), t)
		if v.Decision == hook.Allow {
			continue
		}
		r, ok := commandRule(cmd)
		if !v.ruleCanAllow || !ok || !t.fitsEvery(r) {
			return nil
		}
		found = append(found, r)
	}
	return found
}

// subcommandWord is a second word that names a subcommand (go build, npm
// run), not a flag, path or argument.
var subcommandWord = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

func commandRule(c command) (Rule, bool) {
	if c.dynamic || len(c.assigns) > 0 || len(c.words) == 0 || strings.ContainsAny(c.words[0], "*() ") {
		return Rule{}, false
	}
	prefix := c.words[0]
	if len(c.words) > 1 && subcommandWord.MatchString(c.words[1]) {
		prefix += " " + c.words[1]
	}
	r, err := ParseRule("Bash(" + prefix + " *)")
	return r, err == nil
}

func (g fileGuard) sessionRules(input json.RawMessage) []Rule {
	in, err := decodeInput[struct {
		Path string `json:"file_path"`
	}](input)
	if err != nil {
		return nil
	}
	t := g.ws.file(in.Path)
	v := g.policy.evaluate(g.tool, g.access, t)
	spec, ok := dirSpec(t.forms[0])
	if !v.ruleCanAllow || !ok {
		return nil
	}
	r, err := ParseRule(ruleTool(g.tool) + "(" + spec + ")")
	if err != nil || !t.fitsEvery(r) {
		return nil
	}
	return []Rule{r}
}

// dirSpec is a path specifier for the file's directory, or the file itself
// when it sits at the workspace root.
func dirSpec(f filePath) (string, bool) {
	if strings.ContainsAny(f.abs, "*?[]{}()") {
		return "", false
	}
	if f.outside {
		dir := path.Dir(f.abs)
		if dir == "/" {
			return "", false
		}
		return "/" + dir + "/**", true
	}
	if dir := path.Dir(f.rel); dir != "." {
		return "/" + dir + "/**", true
	}
	return "/" + f.rel, true
}

func (g plainGuard) sessionRules() []Rule {
	if !g.policy.evaluate(g.tool, g.access, anyCall{}).ruleCanAllow {
		return nil
	}
	r, err := ParseRule(ruleTool(g.tool))
	if err != nil {
		return nil
	}
	return []Rule{r}
}

// ruleTool is how a rule names a tool: Bash, Edit; MCP tools as they are.
func ruleTool(name string) string {
	if strings.HasPrefix(name, "mcp__") || name == "" {
		return name
	}
	return strings.ToUpper(name[:1]) + name[1:]
}
