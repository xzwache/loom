package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
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

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	user := writeFile(t, dir, "user.json", `{"model": "user-model", "max_turns": 10}`)
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
			want: Config{Model: "env-model", MaxTokens: Default().MaxTokens, MaxTurns: Default().MaxTurns},
		},
		{
			name:  "missing file is skipped",
			paths: []string{missing},
			env:   map[string]string{EnvModel: "env-model"},
			want:  Config{Model: "env-model", MaxTokens: Default().MaxTokens, MaxTurns: Default().MaxTurns},
		},
		{
			name:  "later files override earlier ones field by field",
			paths: []string{user, project},
			want:  Config{Model: "user-model", MaxTokens: Default().MaxTokens, MaxTurns: 20},
		},
		{
			name:  "environment overrides files",
			paths: []string{user},
			env:   map[string]string{EnvModel: "env-model", EnvAPIKey: "sk-test"},
			want:  Config{Model: "env-model", MaxTokens: Default().MaxTokens, MaxTurns: 10, APIKey: "sk-test"},
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
	for _, want := range []string{"model is not set", "max_tokens", "max_turns"} {
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
