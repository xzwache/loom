package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	home := t.TempDir()
	workdir := t.TempDir()
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte(`{"max_turns": -1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"HOME": home, "ANTHROPIC_API_KEY": "sk-secret"}
	getenv := func(k string) string { return env[k] }

	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{name: "version", args: []string{"-version"}, wantCode: exitOK, wantStdout: "loom dev"},
		{name: "print config", args: []string{"-print-config"}, wantCode: exitOK, wantStdout: `"max_turns": 50`},
		{name: "invalid config file", args: []string{"-config", bad}, wantCode: exitError, wantStderr: "max_turns must be positive"},
		{name: "unknown flag", args: []string{"-nope"}, wantCode: exitUsage, wantStderr: "flag provided but not defined"},
		{name: "no task", args: nil, wantCode: exitUsage, wantStderr: "no task given"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(tt.args, &stdout, &stderr, getenv, workdir)
			if code != tt.wantCode {
				t.Errorf("run(%q) = %d, want %d; stderr: %s", tt.args, code, tt.wantCode, stderr.String())
			}
			if !strings.Contains(stdout.String(), tt.wantStdout) {
				t.Errorf("run(%q) stdout = %q, want containing %q", tt.args, stdout.String(), tt.wantStdout)
			}
			if !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("run(%q) stderr = %q, want containing %q", tt.args, stderr.String(), tt.wantStderr)
			}
			if strings.Contains(stdout.String()+stderr.String(), "sk-secret") {
				t.Errorf("run(%q) leaked the API key", tt.args)
			}
		})
	}
}
