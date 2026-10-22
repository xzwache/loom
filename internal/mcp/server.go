// Package mcp connects loom to MCP (Model Context Protocol) servers and offers
// their tools as loom tools.
//
// Load reads .mcp.json files into ServerConfigs, Connect starts or dials one
// server and lists its tools, and each Tool calls back through that server's
// session:
//
//	servers, _ := mcp.Load(userFile, projectFile, trusted, os.LookupEnv)
//	srv, err := mcp.Connect(ctx, "github", servers.Ready["github"], host)
//	defer srv.Close()
//	for _, t := range srv.Tools() { registry.Register(t) } // mcp__github__create_issue, ...
package mcp

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"os/exec"
	"slices"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type Host struct {
	Version string   // loom's version, sent to servers
	Dir     string   // working directory of stdio servers
	Env     []string // base environment of stdio servers, without loom's secrets
	Limits  Limits
}

// Limits apply to every tool call; both must be set.
type Limits struct {
	CallTimeout time.Duration
	MaxOutput   int // characters
}

type Server struct {
	name    string
	session *sdk.ClientSession
	limits  Limits
	tools   []*Tool
}

// Connect starts a stdio server or dials an HTTP one, then lists its tools.
// ctx bounds only the startup; a running server lives until Close.
func Connect(ctx context.Context, name string, cfg ServerConfig, host Host) (*Server, error) {
	var transport sdk.Transport
	switch cfg.transport() {
	case Stdio:
		transport = &sdk.CommandTransport{Command: command(cfg, host)}
	case HTTP:
		transport = &sdk.StreamableClientTransport{Endpoint: cfg.URL, HTTPClient: headerClient(cfg.Headers)}
	default:
		return nil, fmt.Errorf("mcp server %s: unknown type %q", name, cfg.Type)
	}
	return connect(ctx, name, transport, host)
}

func connect(ctx context.Context, name string, transport sdk.Transport, host Host) (*Server, error) {
	client := sdk.NewClient(&sdk.Implementation{Name: "loom", Version: host.Version}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("mcp server %s: connect: %w", name, err)
	}
	s := &Server{name: name, session: session, limits: host.Limits}
	if err := s.listTools(ctx); err != nil {
		_ = session.Close() // the list error is the one to report
		return nil, fmt.Errorf("mcp server %s: %w", name, err)
	}
	return s, nil
}

func (s *Server) Name() string   { return s.name }
func (s *Server) Tools() []*Tool { return s.tools }
func (s *Server) Close() error   { return s.session.Close() }

func (s *Server) listTools(ctx context.Context) error {
	seen := map[string]string{}
	for t, err := range s.session.Tools(ctx, nil) {
		if err != nil {
			return fmt.Errorf("list tools: %w", err)
		}
		tool, err := newTool(s, t)
		if err != nil {
			return err
		}
		if other, dup := seen[tool.spec.Name]; dup {
			return fmt.Errorf("tools %q and %q both become %s", other, t.Name, tool.spec.Name)
		}
		seen[tool.spec.Name] = t.Name
		s.tools = append(s.tools, tool)
	}
	return nil
}

// command is not tied to the startup ctx: exec.CommandContext would kill the
// server when startup ends.
func command(cfg ServerConfig, host Host) *exec.Cmd {
	cmd := exec.Command(cfg.Command, cfg.Args...) //nolint:gosec,noctx // G204: servers come from the user's config on purpose
	cmd.Dir = host.Dir
	cmd.Env = slices.Clone(host.Env)
	for _, k := range slices.Sorted(maps.Keys(cfg.Env)) {
		cmd.Env = append(cmd.Env, k+"="+cfg.Env[k])
	}
	return cmd
}

func headerClient(headers map[string]string) *http.Client {
	if len(headers) == 0 {
		return nil
	}
	return &http.Client{Transport: headerTransport{headers: headers, next: http.DefaultTransport}}
}

type headerTransport struct {
	headers map[string]string
	next    http.RoundTripper
}

func (h headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range h.headers {
		r.Header.Set(k, v)
	}
	return h.next.RoundTrip(r)
}
