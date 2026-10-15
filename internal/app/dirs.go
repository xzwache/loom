package app

import (
	"cmp"
	"path/filepath"
)

// ProjectDir is loom's folder in a workspace.
const ProjectDir = ".loom"

// Dirs are loom's folders in the user's home.
type Dirs struct {
	Home string
	// User holds the user's config, skills, agent types and instructions,
	// e.g. ~/.config/loom.
	User string
	// State holds what loom writes for itself, such as sessions, e.g.
	// ~/.local/state/loom.
	State string
}

// UserDirs finds loom's folders from the environment, following the XDG base
// directory spec.
func UserDirs(getenv func(string) string) Dirs {
	home := getenv("HOME")
	configHome := cmp.Or(getenv("XDG_CONFIG_HOME"), filepath.Join(home, ".config"))
	stateHome := cmp.Or(getenv("XDG_STATE_HOME"), filepath.Join(home, ".local", "state"))
	return Dirs{
		Home:  home,
		User:  filepath.Join(configHome, "loom"),
		State: filepath.Join(stateHome, "loom"),
	}
}
