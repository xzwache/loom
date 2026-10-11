// Command loom is a coding agent that works in the current directory.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/xzwache/loom/internal/config"
)

var version = "dev"

const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

func main() {
	workdir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "loom: %v\n", err)
		os.Exit(exitError)
	}
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv, workdir))
}

func run(args []string, stdout, stderr io.Writer, getenv func(string) string, workdir string) int {
	fs := flag.NewFlagSet("loom", flag.ContinueOnError)
	fs.SetOutput(stderr)
	showVersion := fs.Bool("version", false, "print the version and exit")
	printConfig := fs.Bool("print-config", false, "print the effective configuration as JSON and exit")
	configPath := fs.String("config", "", "additional config file, applied after the default ones")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}

	if *showVersion {
		fmt.Fprintf(stdout, "loom %s\n", version)
		return exitOK
	}

	paths := config.DefaultPaths(configHome(getenv), workdir)
	if *configPath != "" {
		paths = append(paths, *configPath)
	}
	cfg, err := config.Load(paths, getenv)
	if err != nil {
		fmt.Fprintf(stderr, "loom: %v\n", err)
		return exitError
	}

	if *printConfig {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(cfg); err != nil {
			fmt.Fprintf(stderr, "loom: %v\n", err)
			return exitError
		}
		return exitOK
	}

	fmt.Fprintln(stderr, "loom: no task given; the agent loop is not implemented yet")
	return exitUsage
}

func configHome(getenv func(string) string) string {
	if dir := getenv("XDG_CONFIG_HOME"); dir != "" {
		return dir
	}
	return filepath.Join(getenv("HOME"), ".config")
}
