# MCP client

loom connects to MCP (Model Context Protocol) servers and offers their tools to the model next
to `bash` and `read`. Permissions check them, hooks see them, the trace records them. Built on
`feature/mcp`.

MCP servers are separate programs that offer tools: GitHub, a database, a browser, an issue
tracker. Claude Code, Codex, Cursor and others speak the protocol, so one server works with
all of them, and an existing `.mcp.json` works in loom unchanged.

## Setup

**Your own servers** go in `~/.config/loom/mcp.json`. They always run.

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

**A repository's servers** go in `.mcp.json` at the workspace root. They run only after you
allow them by name in **your user config** (`~/.config/loom/config.json`):

```json
{ "mcp": { "enabled_project_servers": ["github"] } }
```

Then allow the tools, or loom asks before each call (and in headless mode `-p`, asking means
denied):

```json
{ "permissions": { "allow": ["mcp__github"] } }
```

### Server entries

| Field | Meaning |
|---|---|
| `type` | `stdio` (default) or `http` (streamable HTTP) |
| `command`, `args` | stdio: the program to start |
| `env` | stdio: variables added to the server's environment |
| `url`, `headers` | http: the endpoint and headers sent with every request |

- `${VAR}` and `${VAR:-default}` are expanded in all of these, so secrets stay out of the
  file. A missing `${VAR}` without a default is an error for that server.
- The same name in both files: yours wins.
- Names must not be empty or contain `__`.

### Settings

In loom's config (`config.json`):

| Setting | Default | Meaning |
|---|---|---|
| `mcp.enabled_project_servers` | none | project servers allowed to run; user config only |
| `mcp.startup_timeout` | `30s` | time for all servers together to start and list their tools |
| `request_timeout` | `10m` | limit for one tool call (shared with model requests) |
| `max_tool_output` | `30000` | characters of a tool result kept (shared with all tools) |
| `child_env.hide` / `child_env.pass` | | which variables child processes see (see Security) |

## Security

A cloned repository's `.mcp.json` could start any program on the machine, and a server's tool
descriptions are text the model trusts. So:

- **Trust by name, from your config only.** A project server runs only when
  `mcp.enabled_project_servers` in your user config names it. A project's own
  `.loom/config.json` or `.loom/config.local.json` setting that field is a config error, so a
  repository cannot allow itself. Skipped servers are logged and reported (`MCPServerSkipped`).
- **Secrets stay out of server processes.** A stdio server gets loom's environment through the
  same filter as bash and hooks (`internal/childenv`, see `docs/processes.md`): names that look
  like secrets (`*_API_KEY`, `*_TOKEN`, `*_SECRET*`, ...) are left out. A server that needs a
  token gets it on purpose through its `env`.
- **`${VAR}` cannot hand out secrets.** loom's own `ANTHROPIC_API_KEY` is never expanded. A
  project server cannot read variables `child_env` hides (`${GH_TOKEN}`) unless your user
  config lists them in `child_env.pass`; the entry fails with stage `config`, naming them.
- **Tools ask by default.** An MCP tool with no rule asks for approval; `acceptEdits` does not
  cover MCP tools.

## Tools

- Each tool is named `mcp__<server>__<tool>`, as in Claude Code. Characters the API does not
  allow become `_`; names are cut to 64 characters. If two tools end up with the same name, or a
  name is already taken, that whole server is left out and reported.
- Description and input schema come from the server unchanged; the model chooses MCP tools from
  them like any other tool.
- A call is found by name in the tool registry. Each tool holds its server's open session and
  the server's own tool name, and sends `tools/call` with that name.
- **Results**: text content is joined; images and audio become a placeholder
  (`[image/png content not shown]`); resource links are listed; embedded text is included. A
  result marked `isError` becomes a failed tool result. Output is cut to `max_tool_output`, and
  compaction offloads it like any output.

### Permissions

Rules work as for other tools, with one addition: `mcp__github` covers all of that server's
tools; `mcp__github__create_issue` covers one. A deny on one tool beats an allow on its server.
`mcp__git` does not cover `mcp__github__*`: a server rule matches only up to the `__`.

| Mode | MCP tool with no rule |
|---|---|
| `default` | asks (denied in headless mode) |
| `acceptEdits` | asks |
| `plan` | denied |
| `bypassPermissions` | allowed |

### Hooks and events

Hooks see MCP calls like any tool call; matchers are regular expressions on the tool name:
`"matcher": "mcp__github__.*"`.

Startup fires one notice per server, for hooks and the trace. None of them carries a server's
command, URL, env or headers.

| Event | Fields | When |
|---|---|---|
| `MCPServerConnected` | `server`, `source`, `transport`, `tools` | started; `tools` are the registered names |
| `MCPServerSkipped` | `server`, `source`, `reason` | a project server you did not allow |
| `MCPServerFailed` | `server`, `source`, `stage`, `error` | `stage` is `config` (bad entry, missing or refused `${VAR}`) or `connect` (start, handshake, tool list, timeout, name clash) |

`source` is `user` or `project`.

### Subagents

Subagents get MCP tools only when `subagent.tools` lists them, by tool or by server
(`"mcp__github"`); agent type files can name them the same way. An MCP entry whose server did
not start is a warning, not a startup error; an unknown built-in tool still stops startup.

## How a server starts

1. `addMCP` (`internal/app/mcp.go`) runs as a build step after hooks and the session start, so
   its events reach hooks and the trace, and before subagents and the agent, so both see the
   tools.
2. `mcp.Load` reads both files and sorts servers into ready, untrusted (skipped) and invalid
   (failed, stage `config`), each with its source.
3. All ready servers connect at once, under one `startup_timeout`:
   - stdio: `exec.Command(command, args...)` in the workspace, with the filtered environment
     plus the server's `env`; the SDK's `CommandTransport` speaks JSON-RPC over its stdin and
     stdout. The process is not tied to the startup deadline, only the handshake is.
   - http: the SDK's `StreamableClientTransport` to `url`, adding `headers` to each request.
   - Then the handshake and `tools/list`, read once and kept.
4. Each server's tools are registered all together or not at all; `Close` is added to the
   app's cleanup, so stdio servers stop when loom exits.
5. A server that fails is reported and left out; loom runs without it. A broken `.mcp.json`
   (invalid JSON) stops startup, like a broken `config.json`.

## Code

The official Go SDK does the protocol: `github.com/modelcontextprotocol/go-sdk/mcp`.

| File | What |
|---|---|
| `internal/mcp/config.go` | `ServerConfig`, validation, `${VAR}` expansion with `Env{Lookup, Refused}` |
| `internal/mcp/load.go` | `Load`: both files, trust, `Servers{Ready, Untrusted, Invalid}`, `Source`, `Stage` |
| `internal/mcp/server.go` | `Connect` (stdio or http), tool list, `Close` |
| `internal/mcp/tool.go` | `Tool` (a `tool.Tool`), `Run`, content to text, `ToolName` |
| `internal/app/mcp.go` | `addMCP`: load, connect in parallel, register, events, cleanup |
| `internal/tool/names.go` | `Covers`: the one rule for `mcp__<server>` in rules and tool lists |
| `internal/childenv` | the environment filter shared with bash and hooks |

Tests: `internal/mcp/mcp_test.go` (config, trust, expansion, names, calls over an in-memory
server) and `internal/app/mcp_test.go` (a whole app with an HTTP MCP server: tools registered
and callable, skipped, failed and refused servers, project trust, subagent tools).

## Later

- **TUI**: ask once to trust a project server (as Claude Code does) and save the answer to
  `enabled_project_servers` in the user config; show `ask` decisions for MCP tools as an
  approval prompt; `/mcp` to list servers, their tools and failures.
- MCP resources and prompts, OAuth for remote servers, servers that change their tool list
  while running (`tools/list_changed`), images passed to the model instead of a placeholder.

## Decisions

1. **Trust for project servers** (2026-10-17): the allow-list in user config only. No
   `-trust-project` flag for now.
2. **Default permission for MCP tools** (2026-10-17): ask, so denied in headless mode unless a
   rule allows. `acceptEdits` does not cover MCP tools.
3. **Secrets** (2026-10-22): child processes get a filtered environment; project servers cannot
   expand hidden variables unless passed by the user.
