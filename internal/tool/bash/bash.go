// Package bash implements the bash tool.
package bash

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/xzwache/loom/internal/childenv"
	"github.com/xzwache/loom/internal/llm"
	"github.com/xzwache/loom/internal/procgroup"
	"github.com/xzwache/loom/internal/tool"
)

type Tool struct {
	Dir       string
	Timeout   time.Duration
	MaxOutput int
	Env       childenv.Filter // hides secrets from commands; the zero value hides the defaults
}

var _ tool.Tool = (*Tool)(nil)

var errCommandTimeout = errors.New("command timed out")

var bashSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"command": {"type": "string", "description": "The bash command to run."}
	},
	"required": ["command"],
	"additionalProperties": false
}`)

func (t *Tool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: tool.Bash,
		Description: "Run a bash command in the working directory and return its combined stdout and stderr. " +
			"Each call starts a fresh shell; state such as cd or exported variables does not persist.",
		InputSchema: bashSchema,
	}
}

func (t *Tool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if in.Command == "" {
		return "", errors.New("command is empty")
	}

	ctx, cancel := context.WithTimeoutCause(ctx, t.Timeout, errCommandTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", in.Command) //nolint:gosec // G204: running the model's command is this tool's job
	cmd.Dir = t.Dir
	cmd.Env = t.Env.Apply(os.Environ())
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	procgroup.KillOnCancel(cmd)
	cmd.WaitDelay = time.Second

	err := cmd.Run()
	output := tool.Truncate(out.String(), t.MaxOutput)
	switch {
	case errors.Is(context.Cause(ctx), errCommandTimeout):
		return "", fmt.Errorf("command timed out after %s\n%s", t.Timeout, output)
	case ctx.Err() != nil:
		return "", fmt.Errorf("command cancelled\n%s", output)
	case err != nil:
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", fmt.Errorf("exit code %d\n%s", exitErr.ExitCode(), output)
		}
		return "", fmt.Errorf("run command: %w", err)
	}
	if output == "" {
		return "(no output)", nil
	}
	return output, nil
}
