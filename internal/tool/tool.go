// Package tool defines the Tool interface, the registry and the file system workspace.
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xzwache/loom/internal/llm"
)

type Tool interface {
	Spec() llm.ToolSpec
	Run(ctx context.Context, input json.RawMessage) (string, error)
}

// Parallel marks a tool whose calls may run at the same time as other
// Parallel calls in the same turn, such as the agent tool: each subagent
// works in its own context.
type Parallel interface {
	Tool
	RunsInParallel()
}

func Truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	half := limit / 2
	marker := fmt.Sprintf("\n... [%d bytes truncated] ...\n", len(s)-2*half)
	return strings.ToValidUTF8(s[:half], "") + marker + strings.ToValidUTF8(s[len(s)-half:], "")
}
