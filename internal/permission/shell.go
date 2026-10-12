package permission

import (
	"bytes"
	"fmt"
	"path"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// command is one simple command found in a shell command line.
type command struct {
	assigns []string
	words   []string
	writes  []string
	// dynamic means some word depends on an expansion, so its value is unknown.
	dynamic bool
}

func (c command) String() string {
	return strings.Join(slices.Concat(c.assigns, c.words), " ")
}

// parseCommands returns every simple command in src, including those inside
// subshells, functions, pipelines and command substitutions. Words are
// unquoted, so r”m and "rm" both read as rm, and wrappers such as timeout
// and nohup are removed.
func parseCommands(src string) ([]command, error) {
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(src), "")
	if err != nil {
		return nil, fmt.Errorf("parse command: %w", err)
	}
	var cmds []command
	syntax.Walk(f, func(n syntax.Node) bool {
		st, ok := n.(*syntax.Stmt)
		if !ok {
			return true
		}
		c := command{writes: writeTargets(st.Redirs)}
		switch x := st.Cmd.(type) {
		case *syntax.CallExpr:
			for _, a := range x.Assigns {
				c.assigns = append(c.assigns, source(a))
			}
			for _, w := range x.Args {
				v, ok := literal(w)
				if !ok {
					v, c.dynamic = source(w), true
				}
				c.words = append(c.words, v)
			}
			c.words = stripWrappers(c.words)
		case *syntax.DeclClause, *syntax.LetClause:
			c.words, c.dynamic = []string{source(x)}, true
		default:
			if len(c.writes) == 0 {
				return true
			}
		}
		cmds = append(cmds, c)
		return true
	})
	return cmds, nil
}

func writeTargets(redirs []*syntax.Redirect) []string {
	var targets []string
	for _, r := range redirs {
		t, ok := literal(r.Word)
		if !ok {
			t = source(r.Word)
		}
		switch r.Op {
		case syntax.RdrOut, syntax.AppOut, syntax.RdrInOut, syntax.RdrClob, syntax.RdrAll, syntax.AppAll:
		case syntax.DplOut:
			if t == "-" || strings.Trim(t, "0123456789") == "" {
				continue
			}
		default:
			continue
		}
		targets = append(targets, t)
	}
	return targets
}

// literal returns the value of a word without expansions.
func literal(w *syntax.Word) (string, bool) {
	if w == nil {
		return "", false
	}
	var b strings.Builder
	for _, p := range w.Parts {
		switch x := p.(type) {
		case *syntax.Lit:
			b.WriteString(unescape(x.Value))
		case *syntax.SglQuoted:
			if x.Dollar {
				return "", false
			}
			b.WriteString(x.Value)
		case *syntax.DblQuoted:
			for _, q := range x.Parts {
				lit, ok := q.(*syntax.Lit)
				if !ok || x.Dollar {
					return "", false
				}
				b.WriteString(lit.Value)
			}
		default:
			return "", false
		}
	}
	return b.String(), true
}

func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			if s[i] == '\n' {
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func source(n syntax.Node) string {
	var buf bytes.Buffer
	if err := syntax.NewPrinter().Print(&buf, n); err != nil {
		return "?"
	}
	return buf.String()
}

func stripWrappers(words []string) []string {
	for len(words) > 0 {
		switch words[0] {
		case "timeout":
			words = skipOptions(words[1:], "-s", "-k", "--signal", "--kill-after")
			if len(words) > 0 {
				words = words[1:]
			}
		case "nice":
			words = skipOptions(words[1:], "-n", "--adjustment")
		case "nohup", "time":
			words = skipOptions(words[1:])
		case "stdbuf":
			words = skipOptions(words[1:], "-i", "-o", "-e")
		case "command", "xargs":
			if len(words) < 2 || strings.HasPrefix(words[1], "-") {
				return words
			}
			words = words[1:]
		default:
			return words
		}
	}
	return words
}

// skipOptions drops leading options; those in withValue also take the next word.
func skipOptions(words []string, withValue ...string) []string {
	for len(words) > 0 && strings.HasPrefix(words[0], "-") {
		if words[0] == "--" {
			return words[1:]
		}
		if slices.Contains(withValue, words[0]) && len(words) > 1 {
			words = words[1:]
		}
		words = words[1:]
	}
	return words
}

// safeCommands run without a rule: they only read, or (go test) only run the
// project's own code. Each function vets the arguments.
var safeCommands = map[string]func(args []string) bool{
	"ls": anyArgs, "cat": anyArgs, "head": anyArgs, "tail": anyArgs, "wc": anyArgs,
	"pwd": anyArgs, "echo": anyArgs, "grep": anyArgs, "which": anyArgs, "file": anyArgs,
	"stat": anyArgs, "du": anyArgs, "diff": anyArgs, "basename": anyArgs, "dirname": anyArgs,
	"true": anyArgs, "false": anyArgs, "cd": anyArgs,
	"rg":   without("--pre"),
	"find": without("-exec", "-execdir", "-ok", "-okdir", "-delete", "-fprint", "-fprint0", "-fprintf", "-fls"),
	"git":  subcommand([]string{"status", "diff", "log", "show", "blame", "ls-files", "rev-parse"}, "--output", "--ext-diff", "-O"),
	"go":   subcommand([]string{"test", "vet", "list", "version", "doc"}, "-exec", "-toolexec", "-vettool", "-o"),
}

func anyArgs([]string) bool { return true }

func without(flags ...string) func([]string) bool {
	return func(args []string) bool {
		for _, a := range args {
			for _, f := range flags {
				if a == f || strings.HasPrefix(a, f+"=") {
					return false
				}
			}
		}
		return true
	}
}

func subcommand(allowed []string, flags ...string) func([]string) bool {
	return func(args []string) bool {
		return len(args) > 0 && slices.Contains(allowed, args[0]) && without(flags...)(args[1:])
	}
}

// safe reports whether c may run without a rule: a known safe command with
// literal arguments, no file writes, and no paths outside the workspace.
func (c command) safe() bool {
	if c.dynamic || len(c.assigns) > 0 || len(c.words) == 0 {
		return false
	}
	for _, t := range c.writes {
		if t != "/dev/null" {
			return false
		}
	}
	check, ok := safeCommands[c.words[0]]
	if !ok {
		return false
	}
	for _, a := range c.words[1:] {
		if leavesWorkspace(a) {
			return false
		}
	}
	return check(c.words[1:])
}

func leavesWorkspace(arg string) bool {
	if _, v, ok := strings.Cut(arg, "="); ok && strings.HasPrefix(arg, "-") {
		arg = v
	}
	if arg == "/dev/null" {
		return false
	}
	clean := path.Clean(arg)
	return strings.HasPrefix(arg, "/") || strings.HasPrefix(arg, "~") || clean == ".." || strings.HasPrefix(clean, "../")
}
