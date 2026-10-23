# Child processes

loom starts other programs in three places. All three use Go's `os/exec` and all three get
their environment through the same filter.

| Who | Code | Program | Lives for | Result goes to |
|---|---|---|---|---|
| bash tool | `internal/tool/bash/bash.go` | `bash -c "<model's command>"` | one command | the model, as the tool result |
| command hooks | `internal/hook/command/command.go` | `sh -c "<hook command from config>"` | one event | the hook pipeline, as an `Outcome` |
| MCP stdio servers | `internal/mcp/server.go` | the server's `command` + `args` | the whole session | the model, through each MCP tool call |

## Starting a process

The pattern is the same everywhere: build an `exec.Cmd`, set its fields, then run it.

```go
cmd := exec.CommandContext(ctx, "bash", "-c", in.Command) // program + arguments
cmd.Dir = t.Dir                                           // working directory
cmd.Env = t.Env.Apply(os.Environ())                       // environment (see below)
cmd.Stdin = ...                                           // what the program reads (hooks only)
cmd.Stdout, cmd.Stderr = &out, &errOut                    // where its output goes
err := cmd.Run()                                          // start it and wait for it to exit
```

- `exec.CommandContext` ties the process to `ctx`: when the context ends (timeout, Ctrl-C), Go
  kills the process.
- `bash -c` / `sh -c` run a whole command line through a shell, so pipes, `&&` and globs work.
- `Run` = `Start` (fork + exec) + `Wait` (block until exit). MCP servers use `Start` only,
  through the SDK, because they keep running.

## Environment

A child process inherits its parent's environment unless the parent sets one. This is how
Unix and Windows work, not something Go adds; Go's rule is that `cmd.Env == nil` means
"inherit everything". So loom always sets `cmd.Env`:

```go
cmd.Env = filter.Apply(os.Environ())
```

`internal/childenv.Filter` drops variables that look like secrets:

- `DefaultHide` patterns, case-insensitive: `*_API_KEY`, `*_APIKEY`, `*_ACCESS_KEY`,
  `*_PRIVATE_KEY`, `*_TOKEN`, `*_SECRET`, `*_SECRET_*`, `*_PASSWORD`, `*_PASSWD`,
  `*_CREDENTIALS`.
- `child_env.hide` in config adds patterns; it adds up across files, so a repository can hide
  more but never less.
- `child_env.pass` lets exact names through; only the user config may set it.
- `ANTHROPIC_API_KEY` is always hidden, whatever `pass` says.
- The zero `Filter{}` still hides the defaults, so a forgotten field is safe.
- At startup loom logs the names it hides (never values): `hidden from child processes`.

Hooks also get `LOOM_PROJECT_DIR` and `CLAUDE_PROJECT_DIR`. An MCP server also gets its own
`env` from `.mcp.json`, added after the filter, so `"GITHUB_TOKEN": "${GITHUB_TOKEN}"` hands
a token to that one server on purpose.

## Getting results back

A process gives back three things: **stdout**, **stderr** and an **exit code** (0 = success).
loom captures stdout and stderr in memory buffers (`bytes.Buffer`), and reads the exit code
from the error `Run` returns:

| `err` from `Run` | Meaning |
|---|---|
| `nil` | exit code 0 |
| `*exec.ExitError` | the program ran and exited non-zero; `ExitCode()` gives the number |
| `ctx.Err() != nil` | loom killed it: timeout or cancel |
| anything else | it never started (program not found, bad directory) |

### bash: output for the model

```go
cmd.Stdout = &out
cmd.Stderr = &out // one buffer: output and errors interleaved, as in a terminal
err := cmd.Run()
output := tool.Truncate(out.String(), t.MaxOutput) // keeps start and end, cuts the middle
```

| Outcome | Tool result |
|---|---|
| exit 0 | the output, or `(no output)` |
| exit N | error `exit code N` + output (the model still sees what failed) |
| timeout (`bash_timeout`) | error `command timed out after ...` + output so far |
| cancelled | error `command cancelled` + output so far |

A returned error becomes a tool result with `is_error: true` in the agent loop; the model reads
it like any other result.

Killing: on Unix the command runs in its own process group (`Setpgid`, `internal/procgroup`),
and cancel kills the whole group, so `sleep 100 &` or a test runner's children die too. `WaitDelay` (1s) stops
`Run` from hanging on a child that keeps the output pipe open.

### command hooks: an answer for loom

```go
cmd.Stdin = bytes.NewReader(input) // the event as JSON (Claude Code's field names)
cmd.Stdout, cmd.Stderr = &out, &errOut // kept apart: they mean different things
```

| Outcome | What loom does |
|---|---|
| exit 0 | stdout parsed as JSON: `decision`, `reason`, `additionalContext`, `continue`, ... → `hook.Outcome` |
| exit 2 | block: stderr is the reason; on `PreToolUse`/`PermissionRequest` a deny, else a block shown to the model |
| other exit, timeout, bad JSON | logged as a warning and ignored, as in Claude Code |

The default timeout is 60s; a handler's `timeout` (seconds) overrides it. The pipeline then
drops answer fields the event does not accept (`internal/hook/answer.go`).

Like bash, a hook runs in its own process group (`internal/procgroup`), so a timeout also kills
what it started. stdout and stderr are capped at 1 MiB each; a script that prints more fails
(logged, no answer used), so a half-read answer never counts.

### MCP servers: a session, not a one-shot

A stdio MCP server is started once and kept running. The SDK writes JSON-RPC requests to its
stdin and reads responses from its stdout; loom never parses the raw streams. Results come back
per tool call as `CallToolResult` (text, images, resources), which `internal/mcp/tool.go`
turns into text, truncated to `max_tool_output`. `Close` ends the session and the process when
loom exits. See `docs/mcp.md`.
