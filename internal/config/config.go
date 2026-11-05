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

	"github.com/xzwache/loom/internal/childenv"
	"github.com/xzwache/loom/internal/hook/command"
	"github.com/xzwache/loom/internal/permission"
	"github.com/xzwache/loom/internal/prompt"
	"github.com/xzwache/loom/internal/task"
	"github.com/xzwache/loom/internal/tool"
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
	// Compaction keeps long runs inside the context window.
	Compaction Compaction `json:"compaction"`
	// MCP sets how MCP servers start; the servers are listed in .mcp.json.
	MCP MCP `json:"mcp"`
	// Memory keeps facts across sessions in the user's folder.
	Memory Memory `json:"memory"`
	// Tasks configures the task graph.
	Tasks Tasks `json:"tasks"`
	// ChildEnv hides secrets from bash, hooks and MCP servers. Hide adds up
	// across files; Pass may be set only in the user config.
	ChildEnv childenv.Filter `json:"child_env"`
	// Permission lists and floor rules add up across files; the last file that
	// sets mode wins.
	Permissions permission.Settings `json:"permissions"`
	// Hooks from every file run; a file adds to the hooks of the files before it.
	Hooks  command.Settings `json:"hooks,omitempty"`
	APIKey Secret           `json:"-"`
}

// Subagent limits each subagent run. An empty Model uses the main model, so
// a cheaper model can take over searches and other delegated work.
// MaxParallel caps how many subagents of one turn run at once.
type Subagent struct {
	Model        string   `json:"model,omitempty"`
	MaxParallel  int      `json:"max_parallel"`
	MaxTurns     int      `json:"max_turns"`
	MaxRunTokens int      `json:"max_run_tokens"`
	Tools        []string `json:"tools"`
}

// Compaction starts when the context reaches Threshold of ContextWindow and
// shrinks it under Target of it. Threshold 0 turns it off. Old tool output is
// offloaded to .loom/outputs first, except in the last KeepTurns turns, when
// that frees at least MinOffloadTokens; a summary comes only when that is not
// enough. An empty SummaryModel uses the main model. Offloaded output of other
// sessions is deleted after OutputsRetention.
type Compaction struct {
	ContextWindow    int      `json:"context_window"`
	Threshold        float64  `json:"threshold"`
	Target           float64  `json:"target"`
	KeepTurns        int      `json:"keep_turns"`
	MinOffloadTokens int      `json:"min_offload_tokens"`
	SummaryModel     string   `json:"summary_model,omitempty"`
	OutputsRetention Duration `json:"outputs_retention"`
}

// Memory: Enabled gives the model its memory and the memory tool.
// AutoExtract saves what is worth keeping when a session ends. Once there
// are ConsolidateAt memories, a session that changed one also cleans them up;
// 0 turns that off. Model makes these calls and picks memories when the list
// does not fit the prompt; empty uses the subagent model, then the main one.
type Memory struct {
	Enabled       bool   `json:"enabled"`
	AutoExtract   bool   `json:"auto_extract"`
	ConsolidateAt int    `json:"consolidate_at"`
	Model         string `json:"model,omitempty"`
}

// Tasks: List names a task list that sessions of this workspace share; empty
// gives each session a list of its own. Session lists unused for Retention
// are deleted; named lists are kept.
type Tasks struct {
	List      string   `json:"list,omitempty"`
	Retention Duration `json:"retention"`
}

// MCP: a project server in .mcp.json starts only when EnabledProjectServers
// names it. StartupTimeout bounds connecting to all servers.
type MCP struct {
	EnabledProjectServers []string `json:"enabled_project_servers,omitempty"`
	StartupTimeout        Duration `json:"startup_timeout"`
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
			MaxParallel:  4,
			MaxTurns:     30,
			MaxRunTokens: 500_000,
			Tools:        []string{tool.Bash, tool.Read, tool.Write, tool.Edit, tool.Glob, tool.Task},
		},
		Compaction: Compaction{
			ContextWindow:    200_000,
			Threshold:        0.8,
			Target:           0.5,
			KeepTurns:        5,
			MinOffloadTokens: 2000,
			OutputsRetention: Duration(7 * 24 * time.Hour),
		},
		Memory:      Memory{Enabled: true, AutoExtract: true, ConsolidateAt: 30},
		Tasks:       Tasks{Retention: Duration(30 * 24 * time.Hour)},
		MCP:         MCP{StartupTimeout: Duration(30 * time.Second)},
		Permissions: permission.Settings{Mode: permission.ModeDefault},
	}
}

// File is one config file. Only a User file may set
// mcp.enabled_project_servers, so a repository cannot trust its own servers.
type File struct {
	Path string
	User bool
}

// DefaultPaths lists the config files in the order they apply. userDir is
// loom's folder in the user's config home, e.g. ~/.config/loom.
func DefaultPaths(userDir, workdir string) []File {
	return []File{
		{Path: filepath.Join(userDir, "config.json"), User: true},
		{Path: filepath.Join(workdir, ".loom", "config.json")},
		{Path: filepath.Join(workdir, ".loom", "config.local.json")},
	}
}

func Load(files []File, getenv func(string) string) (Config, error) {
	cfg := Default()
	for _, f := range files {
		if err := mergeFile(&cfg, f); err != nil {
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

func mergeFile(cfg *Config, f File) error {
	path := f.Path
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
	trusted, envBefore := cfg.MCP.EnabledProjectServers, cfg.ChildEnv
	cfg.MCP.EnabledProjectServers, cfg.ChildEnv = nil, childenv.Filter{}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(cfg); err != nil {
		return fmt.Errorf("parse config %s: %w", path, err)
	}
	if cfg.MCP.EnabledProjectServers != nil && !f.User {
		return fmt.Errorf("config %s: mcp.enabled_project_servers may be set only in the user config", path)
	}
	if cfg.MCP.EnabledProjectServers == nil {
		cfg.MCP.EnabledProjectServers = trusted
	}
	if cfg.ChildEnv.Pass != nil && !f.User {
		return fmt.Errorf("config %s: child_env.pass may be set only in the user config", path)
	}
	cfg.ChildEnv.Hide = slices.Concat(envBefore.Hide, cfg.ChildEnv.Hide)
	if cfg.ChildEnv.Pass == nil {
		cfg.ChildEnv.Pass = envBefore.Pass
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
		{"subagent.max_parallel", int64(c.Subagent.MaxParallel)},
		{"subagent.max_turns", int64(c.Subagent.MaxTurns)},
		{"subagent.max_run_tokens", int64(c.Subagent.MaxRunTokens)},
		{"compaction.context_window", int64(c.Compaction.ContextWindow)},
		{"compaction.outputs_retention", int64(c.Compaction.OutputsRetention)},
		{"mcp.startup_timeout", int64(c.MCP.StartupTimeout)},
		{"tasks.retention", int64(c.Tasks.Retention)},
	}
	for _, f := range positive {
		if f.value <= 0 {
			errs = append(errs, fmt.Errorf("%s must be positive", f.name))
		}
	}
	if c.Memory.ConsolidateAt < 0 {
		errs = append(errs, fmt.Errorf("memory.consolidate_at must not be negative, got %d", c.Memory.ConsolidateAt))
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
		if slices.Contains(tool.MainOnly, name) {
			errs = append(errs, fmt.Errorf("subagent.tools cannot include %q", name))
		}
	}
	if err := c.Compaction.validate(); err != nil {
		errs = append(errs, err)
	}
	if err := c.MCP.validate(); err != nil {
		errs = append(errs, err)
	}
	if err := task.ValidateListName(c.Tasks.List); c.Tasks.List != "" && err != nil {
		errs = append(errs, fmt.Errorf("tasks.list: %w", err))
	}
	if err := c.ChildEnv.Validate(); err != nil {
		errs = append(errs, err)
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

func (c Compaction) validate() error {
	switch {
	case c.Threshold < 0 || c.Threshold > 1:
		return fmt.Errorf("compaction.threshold must be between 0 and 1, got %g", c.Threshold)
	case c.Threshold > 0 && (c.Target <= 0 || c.Target >= c.Threshold):
		return fmt.Errorf("compaction.target must be above 0 and below threshold %g, got %g", c.Threshold, c.Target)
	case c.KeepTurns < 0:
		return fmt.Errorf("compaction.keep_turns must not be negative, got %d", c.KeepTurns)
	case c.MinOffloadTokens < 0:
		return fmt.Errorf("compaction.min_offload_tokens must not be negative, got %d", c.MinOffloadTokens)
	}
	return nil
}

func (m MCP) validate() error {
	seen := map[string]bool{}
	for _, name := range m.EnabledProjectServers {
		switch {
		case name == "":
			return errors.New("mcp.enabled_project_servers: server name is empty")
		case seen[name]:
			return fmt.Errorf("mcp.enabled_project_servers: %q is listed twice", name)
		}
		seen[name] = true
	}
	return nil
}
