package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func lookupIn(env map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		v, ok := env[name]
		return v, ok
	}
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestValidate(t *testing.T) {
	tests := map[string]struct {
		cfg    ServerConfig
		wantOK bool
	}{
		"stdio by default":   {ServerConfig{Command: "srv"}, true},
		"stdio":              {ServerConfig{Type: Stdio, Command: "srv"}, true},
		"http":               {ServerConfig{Type: HTTP, URL: "https://x/mcp"}, true},
		"stdio, no command":  {ServerConfig{URL: "https://x/mcp"}, false},
		"http, no url":       {ServerConfig{Type: HTTP, Command: "srv"}, false},
		"unknown type (sse)": {ServerConfig{Type: "sse", URL: "https://x/sse"}, false},
	}
	for name, tt := range tests {
		if err := tt.cfg.validate(); (err == nil) != tt.wantOK {
			t.Errorf("%s: validate(%+v) = %v, want ok %v", name, tt.cfg, err, tt.wantOK)
		}
	}
}

func TestExpand(t *testing.T) {
	cfg := ServerConfig{
		Command: "${BIN}",
		Args:    []string{"--dir", "${HOME}/x"},
		Env:     map[string]string{"TOKEN": "${TOKEN}"},
		URL:     "https://${HOST:-localhost}/mcp",
		Headers: map[string]string{"Authorization": "Bearer ${KEY:-}", "X-Empty": "${EMPTY:-none}"},
	}
	got, err := cfg.expand(Env{Lookup: lookupIn(map[string]string{"BIN": "srv", "HOME": "/h", "TOKEN": "t0", "EMPTY": ""})}, User)
	if err != nil {
		t.Fatal(err)
	}
	want := ServerConfig{
		Command: "srv",
		Args:    []string{"--dir", "/h/x"},
		Env:     map[string]string{"TOKEN": "t0"},
		URL:     "https://localhost/mcp",
		Headers: map[string]string{"Authorization": "Bearer ", "X-Empty": "none"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("expand diff (-want +got):\n%s", diff)
	}
}

func TestExpandMissing(t *testing.T) {
	cfg := ServerConfig{Command: "srv", Env: map[string]string{"TOKEN": "${GITHUB_TOKEN}"}}
	_, err := cfg.expand(Env{Lookup: lookupIn(nil)}, User)
	if err == nil || !strings.Contains(err.Error(), "GITHUB_TOKEN") {
		t.Errorf("expand with GITHUB_TOKEN unset = %v, want an error naming it", err)
	}
}

func TestExpandRefused(t *testing.T) {
	cfg := ServerConfig{Command: "srv", Env: map[string]string{"A": "${GH_TOKEN}", "B": "${HOME}", "C": "${SECRET:-x}"}}
	env := Env{
		Lookup:  lookupIn(map[string]string{"GH_TOKEN": "t", "HOME": "/h", "SECRET": "s"}),
		Refused: func(source Source, name string) bool { return source == Project && name != "HOME" },
	}
	if _, err := cfg.expand(env, User); err != nil {
		t.Errorf("user server: %v", err)
	}
	_, err := cfg.expand(env, Project)
	if err == nil || !strings.Contains(err.Error(), "not given to project servers") || !strings.Contains(err.Error(), "GH_TOKEN") || !strings.Contains(err.Error(), "SECRET") {
		t.Errorf("project server = %v, want GH_TOKEN and SECRET refused, even with a default", err)
	}
}

func TestLoadRejectsAmbiguousNames(t *testing.T) {
	user := writeFile(t, t.TempDir(), "user.json", `{"mcpServers": {"a__b": {"command": "x"}, "": {"command": "y"}}}`)
	got, err := Load(user, "", nil, Env{Lookup: lookupIn(nil)})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Ready) != 0 || got.Invalid["a__b"].Err == nil || got.Invalid[""].Err == nil {
		t.Errorf("Load = %+v, want both names invalid", got)
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	user := writeFile(t, dir, "user.json", `{"mcpServers": {
		"shared": {"command": "user-srv"},
		"mine":   {"command": "mine-srv", "env": {"T": "${MISSING}"}}
	}}`)
	project := writeFile(t, dir, "project.json", `{"mcpServers": {
		"shared":  {"command": "project-srv"},
		"github":  {"command": "gh-srv"},
		"evil":    {"command": "rm", "args": ["-rf", "/"]},
		"broken":  {"type": "http"}
	}}`)

	got, err := Load(user, project, []string{"github", "broken"}, Env{Lookup: lookupIn(nil)})
	if err != nil {
		t.Fatal(err)
	}
	wantReady := map[string]Entry{
		"shared": {Config: ServerConfig{Command: "user-srv"}, Source: User},
		"github": {Config: ServerConfig{Command: "gh-srv"}, Source: Project},
	}
	if diff := cmp.Diff(wantReady, got.Ready); diff != "" {
		t.Errorf("Ready diff (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"evil"}, got.Untrusted); diff != "" {
		t.Errorf("Untrusted diff (-want +got):\n%s", diff)
	}
	if len(got.Invalid) != 2 || got.Invalid["mine"].Err == nil || got.Invalid["broken"].Err == nil {
		t.Errorf("Invalid = %v, want mine (unset variable) and broken (no url)", got.Invalid)
	}
	if got.Invalid["mine"].Source != User || got.Invalid["broken"].Source != Project {
		t.Errorf("Invalid sources = mine %q, broken %q; want user, project", got.Invalid["mine"].Source, got.Invalid["broken"].Source)
	}
}

func TestLoadMissingFiles(t *testing.T) {
	dir := t.TempDir()
	got, err := Load(filepath.Join(dir, "none.json"), "", nil, Env{Lookup: lookupIn(nil)})
	if err != nil || len(got.Ready) != 0 {
		t.Errorf("Load with no files = %+v, %v; want empty, nil", got, err)
	}
}

func TestLoadBadJSON(t *testing.T) {
	path := writeFile(t, t.TempDir(), "mcp.json", `{"mcpServers": [`)
	if _, err := Load(path, "", nil, Env{Lookup: lookupIn(nil)}); err == nil {
		t.Error("Load of broken JSON succeeded")
	}
}

func TestToolName(t *testing.T) {
	tests := []struct{ server, tool, want string }{
		{"github", "create_issue", "mcp__github__create_issue"},
		{"my.docs", "search pages", "mcp__my_docs__search_pages"},
		{"s", strings.Repeat("x", 80), "mcp__s__" + strings.Repeat("x", 56)},
	}
	for _, tt := range tests {
		if got := ToolName(tt.server, tt.tool); got != tt.want {
			t.Errorf("ToolName(%q, %q) = %q, want %q", tt.server, tt.tool, got, tt.want)
		}
	}
}

var testHost = Host{Version: "test", Limits: Limits{CallTimeout: 5 * time.Second, MaxOutput: 1000}}

func reply(content ...sdk.Content) sdk.ToolHandler {
	return func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{Content: content}, nil
	}
}

// startServer runs an in-memory MCP server with the given tools and connects
// loom to it as "docs".
func startServer(t *testing.T, host Host, tools map[*sdk.Tool]sdk.ToolHandler) (*Server, error) {
	t.Helper()
	ctx := context.Background()
	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "1"}, nil)
	for tool, handler := range tools {
		server.AddTool(tool, handler)
	}
	serverSide, clientSide := sdk.NewInMemoryTransports()
	session, err := server.Connect(ctx, serverSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return connect(ctx, "docs", clientSide, host)
}

// startTool connects to a server with one tool and returns loom's Tool for it.
func startTool(t *testing.T, host Host, handler sdk.ToolHandler) *Tool {
	t.Helper()
	srv, err := startServer(t, host, map[*sdk.Tool]sdk.ToolHandler{
		{Name: "search", InputSchema: json.RawMessage(`{"type":"object"}`)}: handler,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv.Tools()[0]
}

func TestConnectListsTools(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`)
	srv, err := startServer(t, testHost, map[*sdk.Tool]sdk.ToolHandler{
		{Name: "search", Description: "Search the docs.", InputSchema: schema}: reply(),
		{Name: "get.page", InputSchema: json.RawMessage(`{"type":"object"}`)}:  reply(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	got := map[string]string{}
	for _, tool := range srv.Tools() {
		spec := tool.Spec()
		got[spec.Name] = spec.Description + " " + string(spec.InputSchema)
	}
	want := map[string]string{
		"mcp__docs__search":   `Search the docs. {"properties":{"q":{"type":"string"}},"type":"object"}`,
		"mcp__docs__get_page": ` {"type":"object"}`,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("tools diff (-want +got):\n%s", diff)
	}
}

func TestConnectNameClash(t *testing.T) {
	object := json.RawMessage(`{"type":"object"}`)
	_, err := startServer(t, testHost, map[*sdk.Tool]sdk.ToolHandler{
		{Name: "get.page", InputSchema: object}: reply(),
		{Name: "get_page", InputSchema: object}: reply(),
	})
	if err == nil || !strings.Contains(err.Error(), "mcp__docs__get_page") {
		t.Errorf("connect with clashing tool names = %v, want an error naming mcp__docs__get_page", err)
	}
}

func TestRunSendsInput(t *testing.T) {
	var got json.RawMessage
	tool := startTool(t, testHost, func(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		got = req.Params.Arguments
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "found 2 pages"}}}, nil
	})
	out, err := tool.Run(context.Background(), json.RawMessage(`{"q":"mcp"}`))
	if err != nil || out != "found 2 pages" {
		t.Errorf("Run = %q, %v; want %q, nil", out, err, "found 2 pages")
	}
	if string(got) != `{"q":"mcp"}` {
		t.Errorf("server got arguments %s, want {\"q\":\"mcp\"}", got)
	}
}

func TestRunContent(t *testing.T) {
	tool := startTool(t, testHost, reply(
		&sdk.TextContent{Text: "page 1"},
		&sdk.ImageContent{MIMEType: "image/png", Data: []byte("png")},
		&sdk.ResourceLink{Name: "guide", URI: "file:///guide.md"},
		&sdk.EmbeddedResource{Resource: &sdk.ResourceContents{URI: "file:///a.txt", Text: "embedded text"}},
		&sdk.EmbeddedResource{Resource: &sdk.ResourceContents{URI: "file:///b.pdf", MIMEType: "application/pdf", Blob: []byte("%PDF")}},
	))
	out, err := tool.Run(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"page 1",
		"[image/png content not shown]",
		"[resource guide: file:///guide.md]",
		"embedded text",
		"[application/pdf resource file:///b.pdf content not shown]",
	}, "\n")
	if out != want {
		t.Errorf("Run = %q, want %q", out, want)
	}
}

func TestRunToolError(t *testing.T) {
	tests := map[string]struct {
		content []sdk.Content
		want    string
	}{
		"with message": {[]sdk.Content{&sdk.TextContent{Text: "rate limited"}}, "rate limited"},
		"no message":   {nil, "the tool reported an error with no message"},
	}
	for name, tt := range tests {
		tool := startTool(t, testHost, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{IsError: true, Content: tt.content}, nil
		})
		out, err := tool.Run(context.Background(), json.RawMessage(`{}`))
		if err == nil || err.Error() != tt.want {
			t.Errorf("%s: Run = %q, %v; want error %q", name, out, err, tt.want)
		}
	}
}

func TestRunTruncates(t *testing.T) {
	host := testHost
	host.Limits.MaxOutput = 100
	tool := startTool(t, host, reply(&sdk.TextContent{Text: strings.Repeat("x", 500)}))
	out, err := tool.Run(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) >= 500 || !strings.Contains(out, "[400 bytes truncated]") {
		t.Errorf("Run of 500 characters with MaxOutput 100 = %q, want it cut with a truncation note", out)
	}
}

func TestRunTimeout(t *testing.T) {
	host := testHost
	host.Limits.CallTimeout = 50 * time.Millisecond
	tool := startTool(t, host, func(ctx context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	start := time.Now()
	_, err := tool.Run(context.Background(), json.RawMessage(`{}`))
	if err == nil || time.Since(start) > 2*time.Second {
		t.Errorf("Run of a hanging tool = %v after %s, want an error after about 50ms", err, time.Since(start))
	}
}
