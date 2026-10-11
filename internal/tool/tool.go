// Package tool defines the tools the agent can call and their built-in
// implementations.
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xzwache/loom/internal/llm"
)

// Tool is something the model can call. An error from Run is reported to the
// model as a failed tool result; it never stops the agent loop.
type Tool interface {
	Spec() llm.ToolSpec
	Run(ctx context.Context, input json.RawMessage) (string, error)
}

// Truncate keeps the head and tail of s within limit bytes, since both the
// start of command output and its last lines (errors, summaries) matter.
func Truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	half := limit / 2
	marker := fmt.Sprintf("\n... [%d bytes truncated] ...\n", len(s)-2*half)
	return strings.ToValidUTF8(s[:half], "") + marker + strings.ToValidUTF8(s[len(s)-half:], "")
}
