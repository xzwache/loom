# TUI plan

A terminal UI for loom that any agent can use: the TUI talks to the agent over a protocol, never
through loom's Go code. Status: plan, agreed 2026-10-31; nothing built yet. Phase 0 is done: the
protocol decisions are in `docs/acp.md` (agreed 2026-11-01).

## Decisions

| Decision | Choice | Why |
|---|---|---|
| How the TUI talks to the agent | **ACP** (Agent Client Protocol): JSON-RPC over stdio | an open standard for agent ↔ client; the TUI can drive other ACP agents, and other ACP clients (Zed) can drive loom |
| Where the TUI lives | **a separate Go module in this repo**, `tui/` with its own `go.mod` | it can import only the protocol, never `internal/`; splitting it into its own repo later is a `git subtree split` |
| Framework | **Bubble Tea** (+ Lip Gloss, Bubbles) | the standard for serious Go TUIs |
| Order | **loom-side groundwork first**, then the TUI | the TUI needs streaming, approvals and cancel, which loom does not have yet |

## Layers

```
┌──────────────────────┐       ACP: JSON-RPC over stdio        ┌──────────────────────┐
│ tui/ (own module)    │ ── initialize, session/new, prompt, ─▶ │ loom acp             │
│ Bubble Tea           │    cancel                              │ (cmd/loom, acp mode) │
│ any ACP agent        │ ◀─ session/update: text chunks, tool ─ │ internal/acp         │
│                      │    calls, diffs, plan; permission      │ internal/app (Conversation)   │
│                      │    requests (the agent asks the TUI)   │                      │
└──────────────────────┘                                        └──────────────────────┘
```

- `loom acp` serves ACP on stdin/stdout, on the same `Conversation` that headless mode builds.
- `loom` with no `-p` starts the TUI and loom in one process, the same protocol over an in-memory
  pipe. So there is one code path, not a second "embedded" API.
- The TUI can also start any other ACP agent as a child process.

## What loom has and lacks today

| Needed | Today | Gap |
|---|---|---|
| streamed text and tool input | done: `llm.Model.Stream`, provider-neutral events (`docs/streaming.md`) | an event per chunk for the UI (display events) |
| the user approves a tool call | `PermissionRequest` hook exists; in `-p` an ask becomes a deny (`agent/tools.go`) | a hook the UI answers, waiting for the user |
| cancel the running turn | the run stops on context cancel (`StopCancelled`) | a cancel the UI can send per prompt, keeping the session usable |
| several prompts in one session | `Conversation.Run` per prompt, session saved after each | fine; needs a long-lived Conversation instead of one `-p` run |
| what to show | events for tools, compaction, tasks, memory; `MessageDisplay` and `Notification` defined but unused | map events to ACP `session/update` |

## Phases

### Phase 0: confirm the protocol (short)

- Read the current ACP spec and the official Go SDK, if there is one (`agentclientprotocol.com`,
  Zed's repos): methods, message shapes, version, what is stable.
- Decide: use the SDK, or a small JSON-RPC layer of our own for the methods we need.
- Write `docs/acp.md`: which ACP methods loom supports, and how loom's events map to them.
- Decided (see `docs/acp.md`): what ACP has no message for (token/cost meter, subagent panels, task graph, memory):
  ACP extension fields (`_meta`), or our own `loom/...` methods that other agents can ignore.

### Phase 1: loom groundwork (no UI)

1. **Streaming in `llm`** (done, `docs/streaming.md`): `Model.Stream` yields provider-neutral
   events; `llm.Collect` builds the same `Response`, so the loop does not change; `llm.Send` is for
   helpers (summaries, memory calls).
2. **Display events** fired by the loop: model text chunks, tool call started / finished (with
   input and result), so a UI never parses history. Probably uses `MessageDisplay` or new events;
   Decided: `ModelTextDelta`, `ModelThinkingDelta`, `ToolInputDelta` (see `docs/acp.md`).
3. **Interactive approvals**: an `Approver` the app takes; the permission step calls it on `ask`
   and waits (with ctx), answers allow once / allow always (adds a session rule) / deny. Headless
   keeps "no one can approve".
4. **Cancel per prompt**: `Conversation.Run` already takes a ctx; the session must stay valid after a
   cancel (a cancelled turn leaves the history ending cleanly, tool calls answered).
5. Tests for each, with the replay model (`llmtest.Replay`).

### Phase 2: `loom acp`

1. `internal/acp`: JSON-RPC over stdio; `initialize`, `session/new`, `session/load` (resume),
   `session/prompt`, `session/cancel`; outgoing `session/update` and `session/request_permission`.
2. Map loom events → ACP updates: text chunks, tool calls with status, edit diffs, plan entries
   (from the todo list and tasks).
3. `cmd/loom`: an `acp` mode (decided: the subcommand `loom acp`).
4. End-to-end test: drive `loom acp` with a scripted client over pipes.
5. Try it from Zed as a real second client.

### Phase 3: TUI stage 1 (`tui/`, own module)

- Bubble Tea app that starts an ACP agent (loom by default, any command by flag).
- Streaming chat; tool calls as collapsible blocks with status; diffs for edits.
- Approval prompt: allow once / always / deny, with the tool input shown.
- Esc cancels the turn; typing while the agent works queues the message.
- Status line: model, permission mode, tokens and cost of the session.
- `loom` with no `-p` opens it in-process.

### Phase 4: TUI stage 2

- Slash commands: `/tasks`, `/memory`, `/mcp`, `/model`, `/resume`, `/mode`, `/help`.
- `@file` mentions with fuzzy search; session picker on start.
- Multi-line input, history, paste of large text.

### Phase 5: TUI stage 3

- A live panel per running subagent (parallel ones side by side).
- Task-graph panel; memory browser and editor.
- Trace viewer and replay of a run from `.loom/runs/`.
- Themes and configurable keybindings.

## Repository layout

```
loom/
  cmd/loom/          loom, loom -p, loom acp
  internal/acp/      the ACP server side (loom as an agent)
  internal/...       unchanged
  tui/               separate module: go.mod "github.com/xzwache/loom/tui"
    cmd/loom-tui/    standalone binary for any ACP agent
    ...              imports only the ACP package, never loom/internal
```

Shared ACP types: both `internal/acp` and `tui/` import `github.com/coder/acp-go-sdk` (decided, see
`docs/acp.md`), so loom needs no public package of its own.

## How we know it works

- Each phase has tests with the replay model (`llmtest.Replay`); `make check` covers both modules.
- Phase 2 is done when Zed can run loom over ACP.
- Phase 3 is done when a whole SWE-bench task can be watched and approved in the TUI.
- The benchmark (`~/swebench-loom`, 10/10 on Haiku 5.5) is rerun after phase 1, since streaming
  touches the model client.

## Also pending

- The 100-task SWE-bench run, moved into the repo as `bench/` (new dataset name
  `SWE-bench/SWE-bench_Verified`, Intel images pulled with `--platform linux/amd64`, image deleted
  after scoring to save disk).
