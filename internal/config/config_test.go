package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/xzwache/loom/internal/hook/command"
	"github.com/xzwache/loom/internal/permission"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func envFrom(m map[string]string) func(string) string {
	return func(key string) string { return m[key] }
}

func withDefaults(modify func(*Config)) Config {
	c := Default()
	modify(&c)
	return c
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	user := writeFile(t, dir, "user.json", `{"model": "user-model", "max_turns": 10, "bash_timeout": "45s"}`)
	project := writeFile(t, dir, "project.json", `{"max_turns": 20}`)
	missing := filepath.Join(dir, "missing.json")

	tests := []struct {
		name  string
		paths []string
		env   map[string]string
		want  Config
	}{
		{
			name: "defaults plus model from env",
			env:  map[string]string{EnvModel: "env-model"},
			want: withDefaults(func(c *Config) { c.Model = "env-model" }),
		},
		{
			name:  "missing file is skipped",
			paths: []string{missing},
			env:   map[string]string{EnvModel: "env-model"},
			want:  withDefaults(func(c *Config) { c.Model = "env-model" }),
		},
		{
			name:  "later files override earlier ones field by field",
			paths: []string{user, project},
			want: withDefaults(func(c *Config) {
				c.Model, c.MaxTurns, c.BashTimeout = "user-model", 20, Duration(45*time.Second)
			}),
		},
		{
			name:  "environment overrides files",
			paths: []string{user},
			env:   map[string]string{EnvModel: "env-model", EnvAPIKey: "sk-test"},
			want: withDefaults(func(c *Config) {
				c.Model, c.MaxTurns, c.BashTimeout, c.APIKey = "env-model", 10, Duration(45*time.Second), "sk-test"
			}),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Load(tt.paths, envFrom(tt.env))
			if err != nil {
				t.Fatalf("Load(%v) error: %v", tt.paths, err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Load(%v) mismatch (-want +got):\n%s", tt.paths, diff)
			}
		})
	}
}

func TestLoadErrors(t *testing.T) {
	dir := t.TempDir()

	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{name: "malformed JSON", content: `{"model":`, wantErr: "parse config"},
		{name: "unknown field", content: `{"bogus": "x"}`, wantErr: `unknown field "bogus"`},
		{name: "api key is not a file setting", content: `{"api_key": "sk"}`, wantErr: `unknown field "api_key"`},
		{name: "invalid value", content: `{"max_turns": 0}`, wantErr: "max_turns must be positive"},
		{name: "invalid duration", content: `{"max_duration": "soon"}`, wantErr: `invalid duration "soon"`},
		{name: "negative retries", content: `{"model": "m", "max_retries": -1}`, wantErr: "max_retries must not be negative"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeFile(t, dir, strings.ReplaceAll(tt.name, " ", "_")+".json", tt.content)
			_, err := Load([]string{path}, envFrom(nil))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Load(%s) error = %v, want containing %q", tt.content, err, tt.wantErr)
			}
		})
	}
}

func TestValidateReportsAllErrors(t *testing.T) {
	err := Config{}.Validate()
	if err == nil {
		t.Fatal("Config{}.Validate() = nil, want error")
	}
	for _, want := range []string{"model is not set", "max_tokens", "max_turns", "max_duration", "bash_timeout"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Config{}.Validate() = %q, want it to mention %q", err, want)
		}
	}
}

func TestDefaultPaths(t *testing.T) {
	got := DefaultPaths("/home/u/.config", "/work")
	want := []string{
		"/home/u/.config/loom/config.json",
		"/work/.loom/config.json",
		"/work/.loom/config.local.json",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("DefaultPaths mismatch (-want +got):\n%s", diff)
	}
}

func TestLoadMergesPermissions(t *testing.T) {
	dir := t.TempDir()
	user := writeFile(t, dir, "user.json", `{"permissions": {"mode": "plan", "allow": ["Bash(go test *)"], "deny": ["Read(.env)"]}}`)
	project := writeFile(t, dir, "project.json", `{"permissions": {"mode": "acceptEdits", "allow": ["Edit(docs/**)"]}}`)

	got, err := Load([]string{user, project}, envFrom(map[string]string{EnvModel: "m"}))
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	want := permission.Settings{
		Mode:  permission.ModeAcceptEdits,
		Allow: []string{"Bash(go test *)", "Edit(docs/**)"},
		Deny:  []string{"Read(.env)"},
	}
	if diff := cmp.Diff(want, got.Permissions); diff != "" {
		t.Errorf("permissions mismatch (-want +got):\n%s", diff)
	}

	bad := writeFile(t, dir, "bad.json", `{"permissions": {"deny": ["Glob(x)"]}}`)
	if _, err := Load([]string{bad}, envFrom(map[string]string{EnvModel: "m"})); err == nil {
		t.Error("Load with a bad rule: error = nil, want error")
	}
}

func TestLoadMergesHooks(t *testing.T) {
	dir := t.TempDir()
	user := writeFile(t, dir, "user.json", `{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "a"}]}]}}`)
	project := writeFile(t, dir, "project.json", `{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "b"}]}],
		"PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "c"}]}]}}`)

	got, err := Load([]string{user, project}, envFrom(map[string]string{EnvModel: "m"}))
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	want := command.Settings{
		"Stop":       {{Hooks: []command.Handler{{Type: "command", Command: "a"}}}, {Hooks: []command.Handler{{Type: "command", Command: "b"}}}},
		"PreToolUse": {{Matcher: "Bash", Hooks: []command.Handler{{Type: "command", Command: "c"}}}},
	}
	if diff := cmp.Diff(want, got.Hooks); diff != "" {
		t.Errorf("hooks mismatch (-want +got):\n%s", diff)
	}

	bad := writeFile(t, dir, "bad.json", `{"hooks": {"Nope": []}}`)
	if _, err := Load([]string{bad}, envFrom(map[string]string{EnvModel: "m"})); err == nil {
		t.Error("Load with an unknown hook event: error = nil, want error")
	}
}
