# MCP client

How loom will connect to MCP servers. Status: design approved (decisions at the end of file).
Built on `feature/mcp`, started from `main`.

## What and why

MCP (Model Context Protocol) servers are separate programs that offer tools: GitHub, a
database, a browser, an issue tracker. Claude Code, Codex, Cursor and others speak it, so one
server works with all of them. loom connects to the servers in its config, and their tools sit
next to `bash` and `read`: the model sees them, permissions check them, hooks see them, the
trace records them.

## Config

Claude Code's format, so an existing `.mcp.json` works unchanged.

- **Project** servers: `.mcp.json` in the workspace root.
- **User** servers: `~/.config/loom/mcp.json`.

```json
{
  "mcpServers": {
    "github": {
      "command": "github-mcp-server",
      "args": ["stdio"],
      "env": { "GITHUB_TOKEN": "${GITHUB_TOKEN}" }
    },
    "docs": {
      "type": "http",
      "url": "https://mcp.example.com/mcp",
      "headers": { "Authorization": "Bearer ${DOCS_KEY:-}" }
    }
  }
}
```

- `type` is `stdio` (default when `command` is set) or `http` (streamable HTTP).
- `${VAR}` and `${VAR:-default}` are expanded from the environment in `command`, `args`, `env`,
  `url` and `headers`, so secrets stay out of the file. A missing `${VAR}` without a default is
  a config error for that server.
- The same server name in both files: the user's wins.

## Security

A cloned repository's `.mcp.json` could start any program on the machine. Claude Code asks
before it runs project servers; loom has no prompt in headless mode, so:

- **User** servers run: the user wrote them.
- **Project** servers run only when the user allowed them by name in their own config, never
  from the project's config:

```json
{ "mcp": { "enabled_project_servers": ["github"] } }
```

  Others are skipped, with a log line saying how to enable them.
- Server processes get loom's environment without loom's own secrets such as the API key
  (the same rule as command hooks), plus what their `env` sets.

## Tools

- Every MCP tool is registered as `mcp__<server>__<tool>`, as in Claude Code. Characters the
  API does not allow are replaced with `_`, and names are capped at 64 characters; two tools
  that end up with the same name are an error for that server.
- The tool's description and JSON input schema come from the server, unchanged.
- **Permissions** work as today. `mcp__github` covers all of a server's tools,
  `mcp__github__create_issue` one tool, in allow, ask and deny lists. With no rule, an MCP tool
  asks, which in headless mode means denied, the same as any tool without a guard today.
- **Hooks** see MCP calls like any tool call; command hook matchers work:
  `"matcher": "mcp__github__.*"`.
- **Results**: text content is joined; images and other content become a short placeholder
  (`[image/png content not shown]`) for now; `isError` becomes a tool error. Output is truncated
  to `max_tool_output`, and compaction offloads it like any output.
- **Subagents** get MCP tools only when `subagent.tools` lists them (`"mcp__github"` for a whole
  server).
- **Timeouts**: connecting and listing tools have a startup timeout per server (default 30s);
  tool calls use `request_timeout`.
- A server that fails to start or to list its tools logs a warning and is left out; loom runs
  without it.

## Code

The official Go SDK does the protocol: `github.com/modelcontextprotocol/go-sdk/mcp` (v1.x).

```
internal/mcp/
  config.go   ServerConfig, Servers, Load(userFile, projectFile, allowed), expand ${VAR}
  server.go   Connect(ctx, name, cfg) (*Server, error): stdio or HTTP; Tools(); Close()
  tool.go     Tool: one MCP tool as a tool.Tool (Spec, Run); name cleaning
```

Core shapes:

```go
type ServerConfig struct {
	Type    string            `json:"type,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// Tool is one MCP tool, called through its server's session.
type Tool struct {
	server string
	spec   llm.ToolSpec
	call   func(ctx context.Context, name string, args json.RawMessage) (*mcp.CallToolResult, error)
}

func (t *Tool) Spec() llm.ToolSpec
func (t *Tool) Run(ctx context.Context, input json.RawMessage) (string, error)
```

Changes elsewhere:

| Where | Change |
|---|---|
| `internal/app/build.go` | new step `addMCP` after `addTools`: load config, connect, register tools, add each `Close` to the closers |
| `internal/config` | `mcp.enabled_project_servers`, `mcp.startup_timeout` |
| `internal/event` | `MCPServerConnected {server, source, transport, tools}`, `MCPServerSkipped {server, source, reason}`, `MCPServerFailed {server, source, stage, error}`: notices for the trace |
| `internal/permission` | nothing new: MCP tools take the default guard (rules only); `mcp__server` matches a server's tools |
| `internal/subagent` | nothing new: `subagent.tools` may name MCP tools or servers |

## Tests

- Config: user and project files, user wins, `${VAR}` and `${VAR:-x}`, a missing variable.
- Trust: a project server not in `enabled_project_servers` is skipped; one listed runs.
- An in-process server (the SDK's in-memory transport): its tools are registered with the right
  names and schemas, a call goes through the agent loop, a deny rule blocks it, `isError` comes
  back as a tool error.
- Name cleaning and the 64-character cap; a server that fails is left out with a warning and
  `MCPServerFailed` in the trace.

## Later

MCP resources and prompts, OAuth for remote servers, servers that change their tool list while
running (`tools/list_changed`), `/mcp` in the TUI.

## Decided (2026-10-17)

1. **Trust for project servers**: the allow-list in user config only
   (`mcp.enabled_project_servers`). No `-trust-project` flag for now.
2. **Default permission for MCP tools**: ask, so denied in headless mode unless a rule allows.
   `acceptEdits` does not cover MCP tools.

---

# Report: 2026-10-14 to 2026-10-17

## Built

| Branch | Status | What |
|---|---|---|
| `feature/app-wiring` | merged | `internal/app`: the app is built from named steps, shared by headless mode and the future TUI. Agent loop split into short steps (`internal/agent/loop.go`). Subagent tokens count in the parent's usage and budget. Typed hook answers: a table of what each event accepts (`internal/hook/answer.go`), ignored answers logged; `Notify` for notices, so a failing hook cannot stop loom. |
| `feature/compaction` | merged (#11) | Step 9. Before a model call, at 80% of the context window, old tool output is offloaded to `.loom/outputs/<session>/` (marker with path and size; read back with `read`), and only if that is not enough the history is summarized (transcript capped at half the window). Down to 50%. On `context_full`, compact once and retry. `PreCompact` can add summary instructions; `PostCompact` reports sizes and failures. Secrets: API key redacted, `.gitignore` in the outputs folder, old sessions pruned after 7 days. |

Docs added: `docs/hooks.md` (how hooks and typed answers work), `docs/compaction.md` (design and
decisions), this file. `docs/PLAN.md` is kept local only.

## Next

1. Build MCP on `feature/mcp`.
2. Parallel subagents; `additionalContext` from command hooks on `PreToolUse`.
3. Optional compaction extras: the original request kept next to the summary, the full
   transcript saved before summarizing, the summary text in the trace.
4. Step 10 memory, step 11 task graph; then the TUI, providers, and the "best results from cheap
   models" work in `docs/PLAN.md`.
