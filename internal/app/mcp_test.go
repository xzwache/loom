package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/xzwache/loom/internal/config"
	"github.com/xzwache/loom/internal/event"
	"github.com/xzwache/loom/internal/hook"
	"github.com/xzwache/loom/internal/llm/llmtest"
)

// mcpServer serves one MCP tool, "search", over HTTP.
func mcpServer(t *testing.T) string {
	t.Helper()
	server := sdk.NewServer(&sdk.Implementation{Name: "docs", Version: "1"}, nil)
	server.AddTool(&sdk.Tool{Name: "search", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "found it"}}}, nil
		})
	ts := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil))
	t.Cleanup(ts.Close)
	return ts.URL
}

// buildWithMCP builds an App with the given user and project MCP files and
// returns the MCP events it fired.
func buildWithMCP(t *testing.T, cfg config.Config, userMCP, projectMCP string) (*builder, []event.Event) {
	t.Helper()
	workdir, user := t.TempDir(), t.TempDir()
	if userMCP != "" {
		writeFile(t, filepath.Join(user, MCPFile), userMCP)
	}
	if projectMCP != "" {
		writeFile(t, filepath.Join(workdir, "."+MCPFile), projectMCP)
	}
	cfg.Model = "test-model"
	b := newBuilder(cfg, Options{Workdir: workdir, Dirs: Dirs{Home: t.TempDir(), User: user, State: t.TempDir()}, Model: llmtest.NewReplay()})
	var events []event.Event
	b.app.hooks.OnAll(hook.Func(func(_ context.Context, e event.Event) (hook.Outcome, error) {
		if strings.HasPrefix(string(e.Name()), "MCP") {
			events = append(events, e)
		}
		return hook.Outcome{}, nil
	}))
	a, err := b.build(context.Background())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	t.Cleanup(a.Close)
	return b, events
}

func TestMCPConnectsAndRegistersTools(t *testing.T) {
	url := mcpServer(t)
	b, events := buildWithMCP(t, config.Default(), `{"mcpServers": {"docs": {"type": "http", "url": "`+url+`"}}}`, "")

	want := []event.Event{event.MCPServerConnected{Server: "docs", Source: "user", Transport: "http", Tools: []string{"mcp__docs__search"}}}
	if diff := cmp.Diff(want, events); diff != "" {
		t.Errorf("events (-want +got):\n%s", diff)
	}
	tool, ok := b.tools.Get("mcp__docs__search")
	if !ok {
		t.Fatal("mcp__docs__search is not registered")
	}
	out, err := tool.Run(context.Background(), json.RawMessage(`{}`))
	if err != nil || out != "found it" {
		t.Errorf("Run = %q, %v; want found it", out, err)
	}
}

func TestMCPReportsServersThatDoNotStart(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	user := `{"mcpServers": {
		"broken":  {"type": "http"},
		"missing": {"command": "/does/not/exist"},
		"leaky":   {"command": "srv", "env": {"K": "${ANTHROPIC_API_KEY}"}}
	}}`
	project := `{"mcpServers": {"evil": {"command": "rm"}}}`
	_, events := buildWithMCP(t, config.Default(), user, project)

	got := map[string]string{}
	for _, e := range events {
		switch e := e.(type) {
		case event.MCPServerSkipped:
			got[e.Server] = "skipped " + e.Source
		case event.MCPServerFailed:
			got[e.Server] = "failed " + e.Source + " " + e.Stage
		default:
			t.Errorf("unexpected event %#v", e)
		}
	}
	want := map[string]string{
		"evil":    "skipped project",
		"broken":  "failed user config",
		"leaky":   "failed user config",
		"missing": "failed user connect",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("events (-want +got):\n%s", diff)
	}
}

func TestMCPProjectServerCannotReadHiddenVariables(t *testing.T) {
	t.Setenv("GH_TOKEN", "t")
	url := mcpServer(t)
	project := `{"mcpServers": {"docs": {"type": "http", "url": "` + url + `", "headers": {"Authorization": "Bearer ${GH_TOKEN}"}}}}`

	cfg := config.Default()
	cfg.MCP.EnabledProjectServers = []string{"docs"}
	_, events := buildWithMCP(t, cfg, "", project)
	if len(events) != 1 {
		t.Fatalf("events = %v, want one", events)
	}
	if e, ok := events[0].(event.MCPServerFailed); !ok || e.Stage != "config" || !strings.Contains(e.Error, "GH_TOKEN") {
		t.Errorf("event = %#v, want config failure naming GH_TOKEN", events[0])
	}

	cfg.ChildEnv.Pass = []string{"GH_TOKEN"}
	_, events = buildWithMCP(t, cfg, "", project)
	if len(events) != 1 || events[0].Name() != (event.MCPServerConnected{}).Name() {
		t.Errorf("with GH_TOKEN passed: events = %v, want connected", events)
	}
}

func TestSubagentToolsNameMCPServers(t *testing.T) {
	url := mcpServer(t)
	cfg := config.Default()
	cfg.Subagent.Tools = []string{"read", "mcp__docs", "mcp__offline__search"}
	if _, events := buildWithMCP(t, cfg, `{"mcpServers": {"docs": {"type": "http", "url": "`+url+`"}}}`, ""); len(events) != 1 {
		t.Errorf("events = %v, want docs connected", events)
	}

	cfg.Subagent.Tools = []string{"read", "nope"}
	workdir := t.TempDir()
	cfg.Model = "test-model"
	_, err := Build(context.Background(), cfg, Options{Workdir: workdir, Dirs: Dirs{Home: t.TempDir(), User: t.TempDir(), State: t.TempDir()}, Model: llmtest.NewReplay()})
	if err == nil || !strings.Contains(err.Error(), `unknown tool "nope"`) {
		t.Errorf("Build with an unknown built-in tool = %v, want error", err)
	}
}

func TestMCPTrustedProjectServer(t *testing.T) {
	url := mcpServer(t)
	cfg := config.Default()
	cfg.MCP.EnabledProjectServers = []string{"docs"}
	_, events := buildWithMCP(t, cfg, "", `{"mcpServers": {"docs": {"type": "http", "url": "`+url+`"}}}`)

	if len(events) != 1 {
		t.Fatalf("events = %v, want one MCPServerConnected", events)
	}
	if e, ok := events[0].(event.MCPServerConnected); !ok || e.Source != "project" {
		t.Errorf("event = %#v, want MCPServerConnected from project", events[0])
	}
}
