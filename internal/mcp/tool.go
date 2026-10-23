package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/xzwache/loom/internal/llm"
	"github.com/xzwache/loom/internal/tool"
)

var _ tool.Tool = (*Tool)(nil)

// Tool is one MCP tool, called through its server's session.
type Tool struct {
	server *Server
	name   string // the server's name for the tool; spec.Name is loom's
	spec   llm.ToolSpec
}

func newTool(server *Server, t *sdk.Tool) (*Tool, error) {
	schema, err := json.Marshal(t.InputSchema)
	if err != nil {
		return nil, fmt.Errorf("tool %s: input schema: %w", t.Name, err)
	}
	return &Tool{
		server: server,
		name:   t.Name,
		spec:   llm.ToolSpec{Name: ToolName(server.name, t.Name), Description: t.Description, InputSchema: schema},
	}, nil
}

func (t *Tool) Spec() llm.ToolSpec { return t.spec }

// Run calls the tool on its server. A result the server marks as an error
// is returned as an error, so the model sees a failed tool result.
func (t *Tool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, t.server.limits.CallTimeout)
	defer cancel()
	res, err := t.server.session.CallTool(ctx, &sdk.CallToolParams{Name: t.name, Arguments: input})
	if err != nil {
		return "", fmt.Errorf("mcp server %s: call %s: %w", t.server.name, t.name, err)
	}
	out := tool.Truncate(resultText(res.Content), t.server.limits.MaxOutput)
	if res.IsError {
		if out == "" {
			out = "the tool reported an error with no message"
		}
		return "", errors.New(out)
	}
	return out, nil
}

// resultText joins text content. loom does not pass other content to the
// model yet, so each other part becomes a placeholder saying what was left out.
func resultText(content []sdk.Content) string {
	parts := make([]string, 0, len(content))
	for _, c := range content {
		parts = append(parts, partText(c))
	}
	return strings.Join(parts, "\n")
}

func partText(c sdk.Content) string {
	switch c := c.(type) {
	case *sdk.TextContent:
		return c.Text
	case *sdk.ImageContent:
		return notShown(c.MIMEType)
	case *sdk.AudioContent:
		return notShown(c.MIMEType)
	case *sdk.ResourceLink:
		return fmt.Sprintf("[resource %s: %s]", c.Name, c.URI)
	case *sdk.EmbeddedResource:
		if c.Resource == nil {
			return notShown("resource")
		}
		if c.Resource.Text != "" {
			return c.Resource.Text
		}
		return notShown(strings.TrimSpace(c.Resource.MIMEType + " resource " + c.Resource.URI))
	default:
		return notShown(fmt.Sprintf("%T", c))
	}
}

func notShown(what string) string { return "[" + what + " content not shown]" }

// maxToolName is the API's limit on tool names.
const maxToolName = 64

// ToolName is loom's name for a server's tool: mcp__<server>__<tool>, with
// characters the API does not allow replaced by "_", cut to 64 characters.
func ToolName(server, toolName string) string {
	name := tool.MCPPrefix + clean(server) + "__" + clean(toolName)
	if len(name) > maxToolName {
		name = name[:maxToolName]
	}
	return name
}

func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '_' || r == '-' || ('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z') || ('0' <= r && r <= '9') {
			return r
		}
		return '_'
	}, s)
}
