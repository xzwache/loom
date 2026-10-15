// Command loom is a coding agent that works in the current directory.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/xzwache/loom/internal/app"
	"github.com/xzwache/loom/internal/config"
	"github.com/xzwache/loom/internal/llm"
	"github.com/xzwache/loom/internal/permission"
)

var version = "dev"

const (
	exitOK          = 0
	exitError       = 1
	exitUsage       = 2
	exitIncomplete  = 3
	exitInterrupted = 130
)

type env struct {
	stdout   io.Writer
	stderr   io.Writer
	getenv   func(string) string
	workdir  string
	newModel func(config.Config) llm.Model
}

func main() {
	workdir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "loom: %v\n", err)
		os.Exit(exitError)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], env{
		stdout:   os.Stdout,
		stderr:   os.Stderr,
		getenv:   os.Getenv,
		workdir:  workdir,
		newModel: newAnthropic,
	})
	stop()
	os.Exit(code)
}

func newAnthropic(cfg config.Config) llm.Model {
	return llm.NewAnthropic(llm.AnthropicOptions{
		APIKey:     cfg.APIKey.Reveal(),
		Timeout:    cfg.RequestTimeout.Std(),
		MaxRetries: cfg.MaxRetries,
	})
}

func run(ctx context.Context, args []string, e env) int {
	fs := flag.NewFlagSet("loom", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	showVersion := fs.Bool("version", false, "print the version and exit")
	printConfig := fs.Bool("print-config", false, "print the effective configuration as JSON and exit")
	configPath := fs.String("config", "", "additional config file, applied after the default ones")
	task := fs.String("p", "", "run this task without interaction and print the result")
	jsonOut := fs.Bool("json", false, "with -p, print the result as JSON")
	quiet := fs.Bool("quiet", false, "log only warnings and errors")
	traceRun := fs.Bool("trace", false, "with -p, record every event to .loom/runs/<run-id>.jsonl")
	resume := fs.String("resume", "", "with -p, continue the session with this id")
	latest := fs.Bool("continue", false, "with -p, continue the most recent session")
	permissionMode := fs.String("permission-mode", "", "default, acceptEdits, plan or bypassPermissions; overrides the config")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}

	if *showVersion {
		fmt.Fprintf(e.stdout, "loom %s\n", version)
		return exitOK
	}

	if *task == "" && !*printConfig {
		fmt.Fprintln(e.stderr, `loom: no task given; run loom -p "<task>"`)
		return exitUsage
	}

	userDirs := app.UserDirs(e.getenv)
	user := userDirs.User
	paths := config.DefaultPaths(user, e.workdir)
	if *configPath != "" {
		paths = append(paths, *configPath)
	}

	cfg, err := config.Load(paths, e.getenv)
	if err == nil && *permissionMode != "" {
		cfg.Permissions.Mode = permission.Mode(*permissionMode)
		err = cfg.Validate()
	}
	if err != nil {
		fmt.Fprintf(e.stderr, "loom: %v\n", err)
		return exitError
	}

	if *printConfig {
		enc := json.NewEncoder(e.stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(cfg); err != nil {
			fmt.Fprintf(e.stderr, "loom: %v\n", err)
			return exitError
		}
		return exitOK
	}

	if cfg.APIKey == "" {
		fmt.Fprintf(e.stderr, "loom: %s is not set\n", config.EnvAPIKey)
		return exitError
	}

	level := slog.LevelInfo
	if *quiet {
		level = slog.LevelWarn
	}
	log := slog.New(slog.NewTextHandler(e.stderr, &slog.HandlerOptions{Level: level}))
	return runHeadless(ctx, cfg, headlessOptions{task: *task, jsonOut: *jsonOut, trace: *traceRun, resume: *resume, latest: *latest}, log, e)
}
