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
	"time"
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
	APIKey         Secret   `json:"-"`
}

func Default() Config {
	return Config{
		MaxTokens:      16000,
		MaxTurns:       50,
		MaxRunTokens:   2_000_000,
		MaxDuration:    Duration(30 * time.Minute),
		RequestTimeout: Duration(10 * time.Minute),
		MaxRetries:     2,
		BashTimeout:    Duration(2 * time.Minute),
		MaxToolOutput:  30_000,
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
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(cfg); err != nil {
		return fmt.Errorf("parse config %s: %w", path, err)
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
	}
	for _, f := range positive {
		if f.value <= 0 {
			errs = append(errs, fmt.Errorf("%s must be positive", f.name))
		}
	}
	if c.MaxRetries < 0 {
		errs = append(errs, fmt.Errorf("max_retries must not be negative, got %d", c.MaxRetries))
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	return nil
}
