package permission

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/xzwache/loom/internal/hook"
)

func TestSessionRules(t *testing.T) {
	dir := t.TempDir()
	settings := Settings{
		Allow: []string{"Bash(go build *)"},
		Ask:   []string{"Bash(git commit --amend *)", "mcp__github__delete_repo"},
		Deny:  []string{"Bash(git push *)"},
	}
	tests := []struct {
		name  string
		mode  Mode
		tool  string
		input json.RawMessage
		want  []string
	}{
		{name: "command and subcommand", tool: "bash", input: bash("make install"), want: []string{"Bash(make install *)"}},
		{name: "subcommand of a tool", tool: "bash", input: bash("npm run build --watch"), want: []string{"Bash(npm run *)"}},
		{name: "flag is not a subcommand", tool: "bash", input: bash("curl -s example.com"), want: []string{"Bash(curl *)"}},
		{name: "path is not a subcommand", tool: "bash", input: bash("python ./x.py"), want: []string{"Bash(python *)"}},
		{name: "only the parts that ask", tool: "bash", input: bash("go build . && make test"), want: []string{"Bash(make test *)"}},
		{name: "each part that asks", tool: "bash", input: bash("make a && make a && curl x"), want: []string{"Bash(make a *)", "Bash(curl x *)"}},
		{name: "wrapper is dropped", tool: "bash", input: bash("timeout 5 make install"), want: []string{"Bash(make install *)"}},
		{name: "allowed already", tool: "bash", input: bash("ls -la")},
		{name: "denied", tool: "bash", input: bash("make && git push")},
		{name: "ask rule asks again", tool: "bash", input: bash("git commit --amend")},
		{name: "expansion", tool: "bash", input: bash("$TOOL run")},
		{name: "assignment", tool: "bash", input: bash("FOO=1 make")},
		{name: "unparseable", tool: "bash", input: bash("echo 'open")},
		{name: "floor", tool: "bash", input: bash("rm -rf /")},
		{name: "edit in a directory", tool: "edit", input: file("internal/llm/x.go"), want: []string{"Edit(/internal/llm/**)"}},
		{name: "write at the root", tool: "write", input: file("main.go"), want: []string{"Write(/main.go)"}},
		{name: "glob characters in the path", tool: "edit", input: file("a[1]/x.go")},
		{name: "plan mode denies", mode: ModePlan, tool: "edit", input: file("x.go")},
		{name: "mcp tool", tool: "mcp__github__create_issue", input: json.RawMessage(`{}`), want: []string{"mcp__github__create_issue"}},
		{name: "mcp ask rule", tool: "mcp__github__delete_repo", input: json.RawMessage(`{}`)},
		{name: "unknown tool", tool: "web_fetch", input: json.RawMessage(`{}`), want: []string{"Web_fetch"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := settings
			s.Mode = tt.mode
			p, err := New(s, dir, "")
			if err != nil {
				t.Fatal(err)
			}
			got := p.SessionRules(tt.tool, tt.input)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("SessionRules(%s, %s) (-want +got):\n%s", tt.tool, tt.input, diff)
			}
			if len(got) == 0 {
				return
			}
			if err := p.AllowForSession(got); err != nil {
				t.Fatal(err)
			}
			if v := p.Decide(tt.tool, tt.input); v.Decision != hook.Allow {
				t.Errorf("after allowing %v: Decide = %s (%s), want allow", got, v.Decision, v.Reason)
			}
		})
	}
}

func TestSessionRuleCoversLikeCalls(t *testing.T) {
	p, err := New(Settings{}, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.AllowForSession(p.SessionRules("bash", bash("make install"))); err != nil {
		t.Fatal(err)
	}
	if err := p.AllowForSession(p.SessionRules("edit", file("internal/llm/x.go"))); err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		tool  string
		input json.RawMessage
		want  hook.Decision
	}{
		{"bash", bash("make install DESTDIR=/tmp"), hook.Allow},
		{"bash", bash("make"), hook.Ask},
		{"bash", bash("make clean"), hook.Ask},
		{"edit", file("internal/llm/stream/y.go"), hook.Allow},
		{"edit", file("internal/agent/y.go"), hook.Ask},
		{"write", file("internal/llm/new.go"), hook.Allow}, // Edit rules cover write,
	}
	for _, c := range checks {
		if v := p.Decide(c.tool, c.input); v.Decision != c.want {
			t.Errorf("Decide(%s, %s) = %s, want %s", c.tool, c.input, v.Decision, c.want)
		}
	}
}

func TestSessionRuleNeedsEveryFormOfALink(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../main.go", filepath.Join(dir, "docs", "link.go")); err != nil {
		t.Fatal(err)
	}
	p, err := New(Settings{}, dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := p.SessionRules("edit", file("docs/link.go")); got != nil {
		t.Errorf("SessionRules for a link out of docs = %v, want none", got)
	}
}
