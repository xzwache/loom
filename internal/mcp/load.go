package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
)

// Source is the file a server came from.
type Source string

const (
	User    Source = "user"    // ~/.config/loom/mcp.json
	Project Source = "project" // .mcp.json in the workspace
)

// Stage is where a server failed: in its config entry, or while connecting.
type Stage string

const (
	StageConfig  Stage = "config"
	StageConnect Stage = "connect"
)

type Entry struct {
	Config ServerConfig
	Source Source
}

type BadEntry struct {
	Source Source
	Err    error
}

type Servers struct {
	Ready     map[string]Entry
	Untrusted []string            // project servers not in enabled_project_servers; not started
	Invalid   map[string]BadEntry // bad entries; not started
}

type file struct {
	Servers map[string]ServerConfig `json:"mcpServers"`
}

func Load(userFile, projectFile string, trusted []string, lookup func(string) (string, bool)) (Servers, error) {
	user, err := readFile(userFile)
	if err != nil {
		return Servers{}, err
	}
	project, err := readFile(projectFile)
	if err != nil {
		return Servers{}, err
	}

	found := Servers{Ready: map[string]Entry{}, Invalid: map[string]BadEntry{}}
	for name, cfg := range project {
		if _, mine := user[name]; mine {
			continue
		}
		if !slices.Contains(trusted, name) {
			found.Untrusted = append(found.Untrusted, name)
			continue
		}
		found.add(name, cfg, Project, lookup)
	}
	for name, cfg := range user {
		found.add(name, cfg, User, lookup)
	}
	slices.Sort(found.Untrusted)
	return found, nil
}

func (s *Servers) add(name string, cfg ServerConfig, source Source, lookup func(string) (string, bool)) {
	cfg, err := cfg.expand(lookup)
	if err == nil {
		err = cfg.validate()
	}
	if err != nil {
		s.Invalid[name] = BadEntry{Source: source, Err: err}
		return
	}
	s.Ready[name] = Entry{Config: cfg, Source: source}
}

func readFile(path string) (map[string]ServerConfig, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path) //nolint:gosec // G304: MCP config paths are chosen by loom, not by input
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read mcp config: %w", err)
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse mcp config %s: %w", path, err)
	}
	return f.Servers, nil
}
