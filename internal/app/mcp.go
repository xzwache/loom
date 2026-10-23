package app

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/xzwache/loom/internal/childenv"
	"github.com/xzwache/loom/internal/event"
	"github.com/xzwache/loom/internal/mcp"
)

// MCPFile is the MCP server list, in the user's loom folder and the workspace root.
const MCPFile = "mcp.json"

const untrustedReason = "project server not in mcp.enabled_project_servers of the user config"

// addMCP starts the servers from the user's mcp.json and the workspace's
// .mcp.json, all within mcp.startup_timeout, and registers their tools. A
// server that is skipped, misconfigured or fails to start is logged and
// reported as an event; loom runs without it. Servers close with the App.
func (b *builder) addMCP(ctx context.Context) error {
	servers, err := mcp.Load(b.userPath(MCPFile), filepath.Join(b.ws.Dir(), "."+MCPFile), b.cfg.MCP.EnabledProjectServers, lookupForMCP)
	if err != nil {
		return err
	}
	for _, name := range servers.Untrusted {
		b.app.log.Warn("mcp server skipped", "server", name, "reason", untrustedReason)
		b.app.hooks.Notify(ctx, event.MCPServerSkipped{Server: name, Source: string(mcp.Project), Reason: untrustedReason})
	}
	for _, name := range slices.Sorted(maps.Keys(servers.Invalid)) {
		bad := servers.Invalid[name]
		b.mcpFailed(ctx, name, bad.Source, mcp.StageConfig, bad.Err)
	}

	host := mcp.Host{
		Version: b.opts.Version,
		Dir:     b.ws.Dir(),
		Env:     b.cfg.ChildEnv.Apply(os.Environ()),
		Limits:  mcp.Limits{CallTimeout: b.cfg.RequestTimeout.Std(), MaxOutput: b.cfg.MaxToolOutput},
	}
	timeout := b.cfg.MCP.StartupTimeout.Std()
	startCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for _, s := range connectAll(startCtx, servers.Ready, host) {
		if s.err != nil && startCtx.Err() != nil {
			s.err = fmt.Errorf("%w (mcp.startup_timeout is %s)", s.err, timeout)
		}
		if s.err == nil {
			s.err = b.registerMCP(s.server)
		}
		if s.err != nil {
			b.mcpFailed(ctx, s.name, s.entry.Source, mcp.StageConnect, s.err)
			continue
		}
		b.app.closers = append(b.app.closers, func() {
			if err := s.server.Close(); err != nil {
				b.app.log.Warn("mcp server close", "server", s.name, "error", err)
			}
		})
		tools := toolNames(s.server)
		b.app.log.Info("mcp server connected", "server", s.name, "source", s.entry.Source, "tools", len(tools))
		b.app.hooks.Notify(ctx, event.MCPServerConnected{
			Server:    s.name,
			Source:    string(s.entry.Source),
			Transport: s.entry.Config.Transport(),
			Tools:     tools,
		})
	}
	return nil
}

// lookupForMCP expands ${VAR} in .mcp.json. loom's own secrets read as unset,
// so no server config can hand them out.
func lookupForMCP(name string) (string, bool) {
	if childenv.NeverPassed(name) {
		return "", false
	}
	return os.LookupEnv(name)
}

type startedServer struct {
	name   string
	entry  mcp.Entry
	server *mcp.Server
	err    error
}

// connectAll starts every server at once, so the slowest one sets the
// startup time. Results are sorted by name.
func connectAll(ctx context.Context, ready map[string]mcp.Entry, host mcp.Host) []startedServer {
	names := slices.Sorted(maps.Keys(ready))
	out := make([]startedServer, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Go(func() {
			entry := ready[name]
			srv, err := mcp.Connect(ctx, name, entry.Config, host)
			out[i] = startedServer{name: name, entry: entry, server: srv, err: err}
		})
	}
	wg.Wait()
	return out
}

// registerMCP adds all of a server's tools or none of them; when a name is
// taken the server is closed.
func (b *builder) registerMCP(srv *mcp.Server) error {
	for _, t := range srv.Tools() {
		if _, taken := b.tools.Get(t.Spec().Name); taken {
			_ = srv.Close() // the clash is the error to report
			return fmt.Errorf("tool name %s is already taken", t.Spec().Name)
		}
	}
	for _, t := range srv.Tools() {
		if err := b.tools.Register(t); err != nil {
			_ = srv.Close()
			return err
		}
	}
	return nil
}

func (b *builder) mcpFailed(ctx context.Context, name string, source mcp.Source, stage mcp.Stage, err error) {
	b.app.log.Warn("mcp server failed", "server", name, "source", source, "stage", stage, "error", err)
	b.app.hooks.Notify(ctx, event.MCPServerFailed{Server: name, Source: string(source), Stage: string(stage), Error: err.Error()})
}

func toolNames(srv *mcp.Server) []string {
	names := make([]string, 0, len(srv.Tools()))
	for _, t := range srv.Tools() {
		names = append(names, t.Spec().Name)
	}
	return names
}
