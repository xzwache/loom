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
)

// Environment variables read by Load.
const (
	EnvAPIKey = "ANTHROPIC_API_KEY" //nolint:gosec // G101: variable name, not a credential
	EnvModel  = "LOOM_MODEL"
)

// Config is the complete set of loom settings.
type Config struct {
	// Model is the model ID sent to the provider.
	Model string `json:"model"`
	// MaxTokens caps the output tokens of a single model response.
	MaxTokens int `json:"max_tokens"`
	// MaxTurns caps the model calls in one run.
	MaxTurns int `json:"max_turns"`
	// APIKey authenticates with the provider. It is read from the
	// environment only, never from config files.
	APIKey Secret `json:"-"`
}

// Default returns the built-in configuration.
func Default() Config {
	return Config{
		Model:     "claude-sonnet-5-5",
		MaxTokens: 8192,
		MaxTurns:  50,
	}
}

// DefaultPaths returns the config files Load reads by default, lowest
// precedence first: the user file, the project file, and the project's
// uncommitted local file.
func DefaultPaths(configHome, workdir string) []string {
	return []string{
		filepath.Join(configHome, "loom", "config.json"),
		filepath.Join(workdir, ".loom", "config.json"),
		filepath.Join(workdir, ".loom", "config.local.json"),
	}
}

// Load builds a Config from the defaults, the files in paths (missing files
// are skipped), and the environment as seen through getenv. The result is
// validated.
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

// mergeFile decodes the JSON file at path over cfg. Fields absent from the
// file keep their current values.
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

// Validate reports every invalid setting in c.
func (c Config) Validate() error {
	var errs []error
	if c.Model == "" {
		errs = append(errs, errors.New("model is empty"))
	}
	if c.MaxTokens <= 0 {
		errs = append(errs, fmt.Errorf("max_tokens must be positive, got %d", c.MaxTokens))
	}
	if c.MaxTurns <= 0 {
		errs = append(errs, fmt.Errorf("max_turns must be positive, got %d", c.MaxTurns))
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	return nil
}
