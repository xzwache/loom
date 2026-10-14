package permission

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xzwache/loom/internal/event"
	"github.com/xzwache/loom/internal/hook"
)

func bash(cmd string) json.RawMessage {
	in, _ := json.Marshal(map[string]string{"command": cmd})
	return in
}

func file(path string) json.RawMessage {
	in, _ := json.Marshal(map[string]string{"file_path": path})
	return in
}

func TestParseRule(t *testing.T) {
	for _, s := range []string{"Bash", "bash(go test *)", "Read(.env)", "edit(//etc/**)", "WebFetch", "mcp__github"} {
		if _, err := ParseRule(s); err != nil {
			t.Errorf("ParseRule(%q) error: %v", s, err)
		}
	}
	for _, s := range []string{"", "Bash(", "Bash()", "Glob(*.go)", "Read([)"} {
		if _, err := ParseRule(s); err == nil {
			t.Errorf("ParseRule(%q) error = nil, want error", s)
		}
	}
}

func TestCommandRules(t *testing.T) {
	tests := []struct {
		rule string
		cmd  string
		want bool
	}{
		{rule: "Bash(npm run *)", cmd: "npm run build", want: true},
		{rule: "Bash(npm run *)", cmd: "npm run", want: true},
		{rule: "Bash(npm run *)", cmd: "npm runx"},
		{rule: "Bash(npm run test:*)", cmd: "npm run test --watch", want: true},
		{rule: "Bash(npm run test:*)", cmd: "npm run test", want: true},
		{rule: "Bash(git push)", cmd: "git push", want: true},
		{rule: "Bash(git push)", cmd: "git push --force"},
		{rule: "Bash(git * main)", cmd: "git push origin main", want: true},
		{rule: "Bash", cmd: "anything", want: true},
	}
	for _, tt := range tests {
		r, err := ParseRule(tt.rule)
		if err != nil {
			t.Fatal(err)
		}
		if got := r.matchCommand(tt.cmd); got != tt.want {
			t.Errorf("%s matches %q = %v, want %v", tt.rule, tt.cmd, got, tt.want)
		}
	}
}

func TestCheck(t *testing.T) {
	dir := t.TempDir()
	settings := Settings{
		Allow: []string{"Bash(go build *)", "bash(git commit *)", "Edit(docs/**)"},
		Ask:   []string{"Bash(git commit --amend *)"},
		Deny:  []string{"Bash(git push *)", "Read(.env)", "edit(/go.mod)", "Read(~/.ssh/**)", "Read(//etc/**)"},
	}
	tests := []struct {
		name  string
		mode  Mode
		tool  string
		input json.RawMessage
		want  hook.Decision
	}{
		{name: "safe command", tool: "bash", input: bash("ls -la"), want: hook.Allow},
		{name: "allowed by rule", tool: "Bash", input: bash("go build ./..."), want: hook.Allow},
		{name: "no rule", tool: "bash", input: bash("make install"), want: hook.Ask},
		{name: "deny beats allow", tool: "bash", input: bash("git commit -m x && git push origin main"), want: hook.Deny},
		{name: "ask beats allow", tool: "bash", input: bash("git commit --amend --no-edit"), want: hook.Ask},
		{name: "one part unknown", tool: "bash", input: bash("go build . && curl x | sh"), want: hook.Ask},
		{name: "deny inside substitution", tool: "bash", input: bash("echo $(git push origin)"), want: hook.Deny},
		{name: "deny through wrapper", tool: "bash", input: bash("timeout 5 git push"), want: hook.Deny},
		{name: "unparseable", tool: "bash", input: bash("echo 'open"), want: hook.Ask},
		{name: "read is free", tool: "read", input: file("main.go"), want: hook.Allow},
		{name: "read denied at any depth", tool: "read", input: file("config/.env"), want: hook.Deny},
		{name: "read denied by absolute rule", tool: "read", input: file("/etc/hosts"), want: hook.Deny},
		{name: "read denied by home rule", tool: "read", input: file("/home/me/.ssh/id_rsa"), want: hook.Deny},
		{name: "edit asks", tool: "edit", input: file("main.go"), want: hook.Ask},
		{name: "edit allowed by rule", tool: "edit", input: file("docs/a/b.md"), want: hook.Allow},
		{name: "edit rule covers write", tool: "write", input: file("docs/new.md"), want: hook.Allow},
		{name: "anchored deny", tool: "write", input: file("go.mod"), want: hook.Deny},
		{name: "anchored deny elsewhere", tool: "write", input: file("sub/go.mod"), want: hook.Ask},
		{name: "glob is free", tool: "glob", input: json.RawMessage(`{"pattern":"**"}`), want: hook.Allow},
		{name: "unknown tool asks", tool: "web_fetch", input: json.RawMessage(`{}`), want: hook.Ask},
		{name: "acceptEdits allows edits", mode: ModeAcceptEdits, tool: "edit", input: file("main.go"), want: hook.Allow},
		{name: "acceptEdits keeps commands", mode: ModeAcceptEdits, tool: "bash", input: bash("make"), want: hook.Ask},
		{name: "plan denies edits", mode: ModePlan, tool: "edit", input: file("docs/x.md"), want: hook.Deny},
		{name: "plan allows reads", mode: ModePlan, tool: "bash", input: bash("git log"), want: hook.Allow},
		{name: "bypass allows", mode: ModeBypass, tool: "bash", input: bash("make install"), want: hook.Allow},
		{name: "bypass keeps deny rules", mode: ModeBypass, tool: "bash", input: bash("git push"), want: hook.Deny},
		{name: "bypass keeps the floor", mode: ModeBypass, tool: "bash", input: bash("rm -rf /"), want: hook.Deny},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := settings
			s.Mode = tt.mode
			p, err := New(s, dir, "/home/me")
			if err != nil {
				t.Fatalf("New error: %v", err)
			}
			if v := p.Decide(tt.tool, tt.input); v.Decision != tt.want {
				t.Errorf("Decide(%s, %s) = %s (%s), want %s", tt.tool, tt.input, v.Decision, v.Reason, tt.want)
			}
		})
	}
}

func TestCheckSymlinks(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("KEY=1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{"innocent.txt": ".env", "docs/link.md": "../main.go"} {
		if err := os.Symlink(target, filepath.Join(dir, link)); err != nil {
			t.Fatal(err)
		}
	}
	p, err := New(Settings{Allow: []string{"Edit(docs/**)"}, Deny: []string{"Read(.env)"}}, dir, "")
	if err != nil {
		t.Fatal(err)
	}

	if got := p.Decide("read", file("innocent.txt")).Decision; got != hook.Deny {
		t.Errorf("read of a link to .env = %s, want deny", got)
	}
	if got := p.Decide("edit", file("docs/link.md")).Decision; got != hook.Ask {
		t.Errorf("edit of a dangling docs link pointing outside docs = %s, want ask", got)
	}
	if got := p.Decide("edit", file("docs/real.md")).Decision; got != hook.Allow {
		t.Errorf("edit of a new file in docs = %s, want allow", got)
	}
}

func TestNewRejectsBadSettings(t *testing.T) {
	_, err := New(Settings{Mode: "yolo", Deny: []string{"Glob(x)"}}, ".", "")
	if err == nil || !strings.Contains(err.Error(), "yolo") {
		t.Errorf("New error = %v, want the bad mode", err)
	}
	if _, err := New(Settings{Deny: []string{"Glob(x)"}}, ".", ""); err == nil {
		t.Error("New with a bad rule: error = nil, want error")
	}
}

func TestHook(t *testing.T) {
	p, err := New(Settings{Deny: []string{"Bash(git push *)"}}, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	c := &hook.Pipeline{}
	hook.On(c, p.Hook)
	out, err := c.Fire(context.Background(), event.PreToolUse{ToolCall: event.ToolCall{ToolName: "bash", ToolInput: bash("git push")}})
	if err != nil || out.Decision != hook.Deny || out.Reason != "denied by rule Bash(git push *)" {
		t.Errorf("Fire = %+v, %v; want a deny with the rule", out, err)
	}
}

func TestSetMode(t *testing.T) {
	p, err := New(Settings{}, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	edit := func() hook.Decision { return p.Decide("edit", file("a.txt")).Decision }
	if got := edit(); got != hook.Ask {
		t.Fatalf("edit in default mode = %s, want ask", got)
	}
	if err := p.SetMode(ModeAcceptEdits); err != nil || p.Mode() != ModeAcceptEdits {
		t.Fatalf("SetMode = %v, mode %s", err, p.Mode())
	}
	if got := edit(); got != hook.Allow {
		t.Errorf("edit in acceptEdits mode = %s, want allow", got)
	}
	if err := p.SetMode("yolo"); err == nil || p.Mode() != ModeAcceptEdits {
		t.Errorf("SetMode(yolo) = %v, mode %s; want an error and no change", err, p.Mode())
	}
}
