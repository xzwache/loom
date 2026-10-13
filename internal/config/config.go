// Package config defines loom's settings and loads them from layered sources.
//
// Sources apply in this order, each overriding the previous one:
// built-in defaults, JSON files (user, then project), then environment
// variables. Command-line flags are applied by the caller.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/xzwache/loom/internal/hook/command"
	"github.com/xzwache/loom/internal/permission"
	"github.com/xzwache/loom/internal/prompt"
)

// Environment variables read by Load.
const (
	EnvAPIKey = "ANTHROPIC_API_KEY" //nolint:gosec // G101: variable name, not a credential
	EnvModel  = "LOOM_MODEL"
)

type Config struct {
	Model          string   `json:"model"`
	MaxTokens      int      `json:"max_tokens"`
	MaxTurns       int      `json:"max_turns"`
	MaxRunTokens   int      `json:"max_run_tokens"`
	MaxDuration    Duration `json:"max_duration"`
	RequestTimeout Duration `json:"request_timeout"`
	MaxRetries     int      `json:"max_retries"`
	BashTimeout    Duration `json:"bash_timeout"`
	MaxToolOutput  int      `json:"max_tool_output"`
	// TodoReminderTurns is how many model turns an open todo list may go
	// unchanged before the model is reminded; 0 turns the reminder off.
	TodoReminderTurns int `json:"todo_reminder_turns"`
	// InstructionFiles are the file names read for project instructions, in
	// order; a later file in the list wins on conflicts.
	InstructionFiles []string `json:"instruction_files"`
	// Subagent configures agents started with the agent tool.
	Subagent Subagent `json:"subagent"`
	// Permission lists and floor rules add up across files; the last file that
	// sets mode wins.
	Permissions permission.Settings `json:"permissions"`
	// Hooks from every file run; a file adds to the hooks of the files before it.
	Hooks  command.Settings `json:"hooks,omitempty"`
	APIKey Secret           `json:"-"`
}

// Subagent limits each subagent run. An empty Model uses the main model, so
// a cheaper model can take over searches and other delegated work.
type Subagent struct {
	Model        string   `json:"model,omitempty"`
	MaxTurns     int      `json:"max_turns"`
	MaxRunTokens int      `json:"max_run_tokens"`
	Tools        []string `json:"tools"`
}

func Default() Config {
	return Config{
		MaxTokens:         16000,
		MaxTurns:          50,
		MaxRunTokens:      2_000_000,
		MaxDuration:       Duration(30 * time.Minute),
		RequestTimeout:    Duration(10 * time.Minute),
		MaxRetries:        2,
		BashTimeout:       Duration(2 * time.Minute),
		MaxToolOutput:     30_000,
		TodoReminderTurns: 3,
		InstructionFiles:  slices.Clone(prompt.DefaultInstructionFiles),
		Subagent: Subagent{
			MaxTurns:     30,
			MaxRunTokens: 500_000,
			Tools:        []string{"bash", "read", "write", "edit", "glob"},
		},
		Permissions: permission.Settings{Mode: permission.ModeDefault},
	}
}

func DefaultPaths(configHome, workdir string) []string {
	return []string{
		filepath.Join(configHome, "loom", "config.json"),
		filepath.Join(workdir, ".loom", "config.json"),
		filepath.Join(workdir, ".loom", "config.local.json"),
	}
}

func Load(paths []string, getenv func(string) string) (Config, error) {
	cfg := Default()
	for _, p := range paths {
		if err := mergeFile(&cfg, p); err != nil {
			return Config{}, err
		}
	}
	if v := getenv(EnvModel); v != "" {
		cfg.Model = v
	}
	if v := getenv(EnvAPIKey); v != "" {
		cfg.APIKey = Secret(v)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func mergeFile(cfg *Config, path string) error {
	data, err := os.ReadFile(path) //nolint:gosec // G304: config paths are chosen by the user on purpose
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	before, hooksBefore := cfg.Permissions, cfg.Hooks
	cfg.Hooks = nil
	cfg.Permissions.Allow, cfg.Permissions.Ask, cfg.Permissions.Deny = nil, nil, nil
	cfg.Permissions.Floor = permission.FloorRules{}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(cfg); err != nil {
		return fmt.Errorf("parse config %s: %w", path, err)
	}
	cfg.Permissions.Allow = slices.Concat(before.Allow, cfg.Permissions.Allow)
	cfg.Permissions.Ask = slices.Concat(before.Ask, cfg.Permissions.Ask)
	cfg.Permissions.Deny = slices.Concat(before.Deny, cfg.Permissions.Deny)
	cfg.Permissions.Floor = permission.FloorRules{
		BlockedCommands: slices.Concat(before.Floor.BlockedCommands, cfg.Permissions.Floor.BlockedCommands),
		ProtectedPaths:  slices.Concat(before.Floor.ProtectedPaths, cfg.Permissions.Floor.ProtectedPaths),
		DevicePrefixes:  slices.Concat(before.Floor.DevicePrefixes, cfg.Permissions.Floor.DevicePrefixes),
	}
	for name, matchers := range hooksBefore {
		if cfg.Hooks == nil {
			cfg.Hooks = command.Settings{}
		}
		cfg.Hooks[name] = slices.Concat(matchers, cfg.Hooks[name])
	}
	return nil
}

func (c Config) Validate() error {
	var errs []error
	if c.Model == "" {
		errs = append(errs, errors.New(`model is not set; set "model" in a config file or `+EnvModel))
	}
	positive := []struct {
		name  string
		value int64
	}{
		{"max_tokens", int64(c.MaxTokens)},
		{"max_turns", int64(c.MaxTurns)},
		{"max_run_tokens", int64(c.MaxRunTokens)},
		{"max_duration", int64(c.MaxDuration)},
		{"request_timeout", int64(c.RequestTimeout)},
		{"bash_timeout", int64(c.BashTimeout)},
		{"max_tool_output", int64(c.MaxToolOutput)},
		{"subagent.max_turns", int64(c.Subagent.MaxTurns)},
		{"subagent.max_run_tokens", int64(c.Subagent.MaxRunTokens)},
	}
	for _, f := range positive {
		if f.value <= 0 {
			errs = append(errs, fmt.Errorf("%s must be positive", f.name))
		}
	}
	if c.TodoReminderTurns < 0 {
		errs = append(errs, fmt.Errorf("todo_reminder_turns must not be negative, got %d", c.TodoReminderTurns))
	}
	for _, name := range c.InstructionFiles {
		if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
			errs = append(errs, fmt.Errorf("instruction_files: %q is not a file name", name))
		}
	}
	for _, name := range c.Subagent.Tools {
		if name == "agent" || name == "todo" {
			errs = append(errs, fmt.Errorf("subagent.tools cannot include %q", name))
		}
	}
	if c.MaxRetries < 0 {
		errs = append(errs, fmt.Errorf("max_retries must not be negative, got %d", c.MaxRetries))
	}
	if err := c.Permissions.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("permissions: %w", err))
	}
	if err := c.Hooks.Validate(); err != nil {
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	return nil
}
