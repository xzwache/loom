package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/xzwache/loom/internal/llm"
)

type Bash struct {
	Dir       string
	Timeout   time.Duration
	MaxOutput int
}

var _ Tool = (*Bash)(nil)

var errCommandTimeout = errors.New("command timed out")

var bashSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"command": {"type": "string", "description": "The bash command to run."}
	},
	"required": ["command"],
	"additionalProperties": false
}`)

func (b *Bash) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: "bash",
		Description: "Run a bash command in the working directory and return its combined stdout and stderr. " +
			"Each call starts a fresh shell; state such as cd or exported variables does not persist.",
		InputSchema: bashSchema,
	}
}

func (b *Bash) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if in.Command == "" {
		return "", errors.New("command is empty")
	}

	ctx, cancel := context.WithTimeoutCause(ctx, b.Timeout, errCommandTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", in.Command) //nolint:gosec // G204: running the model's command is this tool's job
	cmd.Dir = b.Dir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	killProcessGroupOnCancel(cmd)
	// Background children can keep the output pipe open after bash exits.
	cmd.WaitDelay = time.Second

	err := cmd.Run()
	output := Truncate(out.String(), b.MaxOutput)
	switch {
	case errors.Is(context.Cause(ctx), errCommandTimeout):
		return "", fmt.Errorf("command timed out after %s\n%s", b.Timeout, output)
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
