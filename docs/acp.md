# ACP in loom

How loom speaks the Agent Client Protocol (ACP): which methods it supports, and how its events map
to ACP messages. Phase 0 of `docs/tui.md`. Status: plan, written 2026-10-31, decisions agreed 2026-11-01; nothing built yet.

Spec read on 2026-10-31: https://agentclientprotocol.com/protocol/v1/overview, schema
`schema/v1/schema.json` in https://github.com/agentclientprotocol/agent-client-protocol.

## The protocol in short

- JSON-RPC 2.0 over stdio. The client starts the agent as a child process; one JSON message per
  line on stdin/stdout. stdout carries only ACP messages; logs go to **stderr**.
- Both sides send requests. The client asks the agent to prompt; the agent asks the client for
  permission, and streams `session/update` notifications while it works.
- Wire version **1**. New features come as capabilities, not version bumps. The current v1 schema
  is 1.28.0. A v2 is in draft (alpha); loom targets **v1** and ignores v2 until it is stable.
- Only `text` and `resource_link` prompt content are required; images, audio and embedded files
  are capabilities.
- Anything custom goes in `_meta` fields (any object can carry one) or in methods whose name starts
  with `_`. Unknown `_meta` keys and `_` notifications are ignored by other clients.

## Go SDK

There is no official Go SDK (official ones: Rust, TypeScript, Python, Kotlin, Java). The community
choice is **`github.com/coder/acp-go-sdk`** (Coder, MIT, v0.13.5 from 2026-06-02, main active).

- Agent side: implement `acp.Agent` (`Initialize`, `NewSession`, `Prompt`, `Cancel`, ...), then
  `acp.NewAgentSideConnection(agent, os.Stdout, os.Stdin)`. The connection also sends to the client:
  `SessionUpdate`, `RequestPermission`. It cancels the `Prompt` ctx on `session/cancel`.
- Client side: implement `acp.Client` (`SessionUpdate`, `RequestPermission`, fs, terminal), then
  `acp.NewClientSideConnection`. This is what `tui/` would use.
- Lag: it is built from an older schema. It lacks the newer `notice` and `compaction_update`
  updates and the tool call `name` field. It has `usage_update`, session list/resume/close and
  config options. It is pre-1.0, so updates can break the API.

**Decided: use the SDK**, pinned, behind `internal/acp`, so no
other loom package imports it. It also settles "where the shared types live" from `docs/tui.md`:
both `internal/acp` and `tui/` import the SDK, and loom needs no public package of its own. Write
our own small layer only if the lag blocks something we need (the two missing updates can be sent
as raw JSON through the SDK's extension calls, or wait for a release).

Not chosen: about 600 lines of our own JSON-RPC and types for the ~12 messages loom uses. More
to learn from, more to maintain, and a third-party dependency avoided.

## Methods loom supports

### Client → loom

| Method | Phase | loom does |
|---|---|---|
| `initialize` | 2 | answers version 1, `agentInfo{name: "loom", version}`, capabilities below, no auth methods |
| `session/new` | 2 | builds a `Conversation` for `cwd` (new session); returns `sessionId` and config options |
| `session/prompt` | 2 | `Conversation.Run` with the prompt text; streams updates; returns the stop reason |
| `session/cancel` | 2 | cancels that prompt's ctx; the prompt answers `cancelled` |
| `session/load` | 2 | builds a `Conversation` with `Resume`, replays the history as updates, then answers |
| `session/list` | 2 | `session.Store.List` for `cwd`: id, title, updated time |
| `session/resume` | 2 | like load, without the replay |
| `session/close` | 2 | cancels any running prompt, `Conversation.End`, `Conversation.Close` |
| `session/set_config_option` | 2 | permission mode (`Policy.SetMode`), model; answers with all options |
| `session/set_mode` | 2 | same as the mode option; deprecated in ACP, kept for older clients |
| `authenticate` | – | not needed: loom reads its key from its own config |
| `session/delete`, `session/fork` | later | delete is stable but not needed for the TUI; fork is unstable |

Capabilities loom sends: `loadSession: true`, `sessionCapabilities{list, resume, close}`,
`promptCapabilities` all false at first (text only), `mcpCapabilities{http: true}`.

One ACP session = one `Conversation`. An ACP connection can hold several sessions, so `internal/acp` keeps a
map from session id to its `Conversation` and its running prompt's cancel func.

### loom → client

| Method | Phase | Used for |
|---|---|---|
| `session/update` | 2 | everything shown: text, tool calls, plan, usage, mode changes |
| `session/request_permission` | 2 | a tool call whose permission is `ask` |
| `fs/*`, `terminal/*` | later | not used: loom's tools read, write and run on disk themselves |

Using the client's files (`fs/read_text_file` returns unsaved editor buffers) and terminals matters
for Zed, not for the TUI. **Later**, as a `tool.Workspace` backed by the client.

## Events → `session/update`

What each loom event becomes. "Phase 1" marks events that do not exist yet (see `docs/tui.md`).

| loom | ACP update | Notes |
|---|---|---|
| model text chunk (phase 1) | `agent_message_chunk` `{content: text}` | |
| model thinking chunk (phase 1) | `agent_thought_chunk` | only when thinking is on |
| prompt replay on `session/load` | `user_message_chunk`, `agent_message_chunk`, `tool_call` | from `History.Messages()` |
| model asks for a tool (`ModelResponse.ToolUses`, or tool input chunk in phase 1) | `tool_call` `{toolCallId, title, kind, status: pending, rawInput}` | `toolCallId` = the tool use id |
| `PreToolUse` allowed, tool starts | `tool_call_update` `{status: in_progress}` | |
| `PermissionRequest` (ask) | `session/request_permission` | see below |
| `PostToolUse` | `tool_call_update` `{status: completed, content, rawOutput}` | edits add a `diff` content; read/edit add `locations` |
| `PostToolUseFailure`, `PermissionDenied` | `tool_call_update` `{status: failed, content: reason}` | |
| `TodoUpdated` | `plan` `{entries}` | carries the items (`Items`) |
| `ModelResponse.Usage` | `usage_update` `{used, size, cost}` | `used` = tokens in context, `size` = window; cost from the model's price |
| permission mode changed | `current_mode_update` and `config_option_update` | |
| `PreCompact` / `PostCompact` | `compaction_update` | only if the client says `session.compaction` |
| `Notification`, `BudgetExceeded`, `MCPServerFailed`, `MCPServerSkipped`, `StopFailure` | `notice` `{severity, title, description}` | only if the client says `session.notices`; else a log line on stderr |
| `SessionStart`, `Stop`, `ModelRequest`, `PostToolBatch`, `SkillLoaded`, `InstructionsLoaded`, `ConfigChange`, ... | nothing | trace and hooks only |
| subagent events (ctx carries `event.Agent`) | see "loom-specific data" | |

### Tool call title and kind

| loom tool | `kind` | `title` |
|---|---|---|
| `read` | `read` | `Read <path>` |
| `glob` | `search` | `Glob <pattern>` |
| `edit`, `write` | `edit` | `Edit <path>`, `Write <path>` |
| `bash` | `execute` | the command, cut to one line |
| `agent` | `other` | `Agent: <description>` |
| `todo` | `think` | `Update todo list` (the list itself goes out as `plan`) |
| `task`, `memory`, `skill` | `other` | `Task: <action>`, `Memory: <action>`, `Skill <name>` |
| `mcp__<server>__<tool>` | `other` | `<server>: <tool>` |

The title and kind come from the tool, not from a table in `internal/acp`: a small optional
interface such as `tool.Describer{Describe(input) (title, kind)}`, so new tools describe
themselves. MCP tools get the generic one.

### Edit diffs

ACP's `diff` content is `{path, oldText, newText}` with the **whole file** before and after
(`oldText` null for a new file). `PostToolUse` has only the tool input (old/new string). So the
edit and write tools must report the file before and after, for example in a structured result
next to the text the model gets. Decided: the tools report the file before and after; the exact
shape is settled in phase 1 with the display events.

### Plan from the todo list

`TodoUpdated` carries `Items` (added for this) besides `total` and `done`: content and status map
one to one to plan entries; `priority` is always `medium`. The task graph is not
the plan: it is shared between sessions and has dependencies, so it goes out as loom-specific data.

## Permissions

loom's `ask` becomes `session/request_permission` with the tool call and these options:

| `optionId` | `kind` | loom does |
|---|---|---|
| `allow` | `allow_once` | runs this call |
| `allow-always` | `allow_always` | adds a session allow rule for this tool (and command prefix for `bash`), runs it |
| `deny` | `reject_once` | denies this call; the model gets "denied by the user" |

The answer `cancelled` (the client cancelled the prompt) denies the call and the turn ends
`cancelled`. ACP leaves "always" to the agent, so the session rule lives in loom's `Policy`, like
Claude Code's session rules. `reject_always` is not offered at first: a deny rule from one click is
easy to set by mistake and hard to see. **Later**, if wanted.

This is phase 1's `Approver`: `internal/acp` implements it by sending the request and waiting for
the answer with the prompt's ctx. Headless keeps the one that always denies.

## Modes and model

ACP's newer way is session config options; the older `modes` still work. loom sends both:

- `configOptions`:
  - `mode`, category `mode`, select: `default`, `acceptEdits`, `plan`, `bypassPermissions`;
  - `model`, category `model`, select: the models in loom's config.
- `modes` with the same four modes, for clients that only know modes.

Changing either calls `Policy.SetMode` or switches the model (firing `PreModelSwitch` /
`PostModelSwitch`), then sends `config_option_update`.

## Stop reasons

| `agent.StopReason` | ACP `stopReason` |
|---|---|
| `done` | `end_turn` |
| `cancelled` | `cancelled` |
| `max_turns` | `max_turn_requests` |
| `truncated` | `max_tokens` |
| `refusal` | `refusal` |
| `max_run_tokens`, `timeout`, `context_full`, `halted` | `end_turn` + a `notice` (error) + `_meta["dev.loom/stop"]` with the real reason |

ACP has no stop reason for the last four. They are not hidden: the notice tells the user, and
`_meta` carries loom's exact reason for clients that read it. A Go `error` from `Conversation.Run` becomes a
JSON-RPC error on the prompt (internal error, -32603).

## MCP servers from the client

`session/new` brings `mcpServers` (stdio, and http when we say we support it). loom adds them to
the servers from its config for that session, as source `client`. A client server with the same
name as a configured one: **the client's wins** (decided).

## loom-specific data

ACP has no message for: the session's total cost, subagent panels, the task graph, memory.
**Decided: both**, each where it fits:

- **On existing messages, `_meta`**: data that belongs to a message ACP already sends.
  - `tool_call` from a subagent: `_meta["dev.loom/agent"] = {id, type}`, so the TUI groups them in a
    subagent panel. ACP's own subagent support is unstable; switch when it is stable.
  - `usage_update`: `_meta["dev.loom/usage"]` with input, output and cache tokens for the session.
- **New notifications, `_dev.loom/...`**: data with no ACP message.
  - `_dev.loom/tasks` — the task list, sent whole when a task changes;
  - `_dev.loom/memory` — a memory written or deleted.
- Advertised in `initialize` as `agentCapabilities._meta["dev.loom"] = {version: 1}`, so the TUI
  knows which extras to expect, and shows nothing extra for other agents.

Other clients ignore all of these, as ACP requires.

## Questions from `docs/tui.md`, decided

| Question | Decision |
|---|---|
| loom-specific data over ACP | `_meta` on existing messages + `_dev.loom/...` notifications (above) |
| display event names | built: `ModelTextDelta`, `ModelThinkingDelta`, `ModelToolUse` (a tool call starts streaming), `ToolRunning` (passed its checks); finish is `PostToolUse` / `PostToolUseFailure` / `PermissionDenied`. Shown through `hook.Pipeline.Show`: observers only, never hooks or the trace. See `docs/frontend.md` |
| `acp`: flag or subcommand | subcommand, `loom acp`: it is a different program (server on stdio), not an option of a run, and it takes no prompt |
| where the shared ACP types live | in `github.com/coder/acp-go-sdk` |

## What phase 2 tests

- A scripted client over `io.Pipe`: initialize, new session, prompt with the replay model, check
  the updates in order (text, tool call pending → in_progress → completed, usage), and the stop reason.
- Permission: a tool that asks; the client answers each option; check the rule and the result.
- Cancel during a tool; the prompt answers `cancelled`, the session takes another prompt.
- Load: the history comes back as updates.
- By hand: Zed runs `loom acp` as a custom agent.
