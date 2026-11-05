# Frontend layer

`internal/frontend` is the Go API every UI drives loom through: open a session, send prompts,
cancel them, and get updates as they happen. ACP (`internal/acp`), headless mode (`-p`) and any
later UI (web, HTTP) are thin translators on top of it. Decided 2026-11-01 over "display events
only" (option B).

Status: built (steps 1 and 2 below, and slash commands); approvals, `-p` and ACP are not.

## Layers

```
 core                     loom events                 frontend                  translators       UIs
┌──────────────┐  Fire   ┌──────────────────┐ observe ┌──────────────────┐ Update ┌─────────────┐   ┌────────────┐
│ agent loop   │───────▶ │ hook.Pipeline    │───────▶ │ internal/frontend│──────▶ │ internal/acp│═══│ tui/, Zed, │
│ llm, tools,  │  Show   │ hooks, trace,    │         │ Session:         │        │             │ACP│ any client │
│ subagents    │───────▶ │ then observers   │         │ Prompt, Cancel,  │──────▶ │ -p printer  │   └────────────┘
└──────────────┘         └──────────────────┘         │ State, Close     │        └─────────────┘
       ▲                                              └────────┬─────────┘
       └──────────── app.Conversation (one session) ◀──────────┘ owns
```

- The core knows nothing about UIs. It fires events, as it does for hooks and the trace.
- `hook.Pipeline` has **observers** besides hooks (`internal/hook/observer.go`). An observer sees
  each event after the hooks ran on it, with the input they rewrote; it cannot change anything.
- `Pipeline.Show` gives an event to the observers only. The loop uses it for events that are only
  for display, so hooks and the trace never get one event per streamed piece.
- `app.Options.Observe` adds an observer when the Conversation is built, so it sees startup events
  too (`SessionStart`, MCP servers that failed).
- `frontend` turns events into `Update`s, tagged with the agent that sent them, and owns the
  long-lived `app.Conversation`.

## Display events (`internal/event`)

Shown only, never hooked; config cannot name them.

| Event | Fired | From |
|---|---|---|
| `ModelTextDelta{Text}` | for each piece of answer text | `llm.TextDelta`, through `llm.Collect`'s callback |
| `ModelThinkingDelta{Thinking}` | for each piece of thinking | `llm.ThinkingDelta` |
| `ModelToolUse{ToolUseID, ToolName}` | when the model starts writing a tool call | `llm.BlockStart` of a `ToolUse` |
| `ToolRunning{ToolCall}` | when a call passed its checks (hooks, permissions) and starts | the loop, before the tool runs |

`TodoUpdated` also gained `Items`, so a UI can show the plan, not just counts.

## API

```go
s, err := frontend.Open(ctx, cfg, opts, ui) // builds the Conversation; updates start during Open
res, err := s.Prompt(ctx, "fix the bug")    // blocks; TurnDone is the last update
s.Prompt(ctx, "/compact")                   // a slash command: no model turn
s.Commands()                                // the commands, for autocomplete
s.Cancel()                                  // stops the running prompt; the session stays usable
st := s.State()                             // what is happening now
s.Close(ctx, frontend.EndOther)             // cancels a running prompt, waits, Conversation.End + Conversation.Close

type UI interface {
	Update(u Update) // one at a time, in order; may call Cancel, not State or Close
}
```

- A second `Prompt` while one runs returns `ErrBusy`; a `Prompt` after the session ended returns
  `ErrClosed`.
- Why a session ended is typed, as `SessionEnd` hooks get it: `EndExit` (`prompt_input_exit`:
  `/exit`, Ctrl+D), `EndClear` (`clear`), `EndOther` (`other`: the UI went away). `-p` keeps its own
  reasons (`done`, `error`, ...): they say how the run ended.
- `UI.Update` runs on the goroutine that made the event, while the Session holds its lock. The
  agent waits for it: a translator only writes (stdout, the ACP connection, Bubble Tea's
  `program.Send`) and hands anything slow off.

### Updates

```go
type Update struct {
	Agent event.Agent // zero for the main agent
	Body  Body
}
```

| Body | From |
|---|---|
| `TextDelta`, `ThinkingDelta` | `ModelTextDelta`, `ModelThinkingDelta` |
| `ToolPending{ID, Name}` | `ModelToolUse` |
| `ToolStarted{ID, Name, Input}` | `ToolRunning` |
| `ToolFinished{ID, Status, Output}` | `PostToolUse` (done), `PostToolUseFailure` (failed), `PermissionDenied` (denied); end of the turn (skipped, cancelled) |
| `PlanChanged{Items}` | `TodoUpdated` |
| `SubagentStarted` / `SubagentStopped` | `SubagentStart` / `SubagentStop` |
| `Compacted{TokensBefore, TokensAfter}` | `PostCompact` |
| `Usage{Usage}` | `ModelResponse`: one model call |
| `Notice{Kind, Message}` | `Notification`, `MCPServerFailed`; what a command reports (kind `command`) |
| `SessionStarted{Source, Model}` | `SessionStart`: at `Open`, and after `/clear` |
| `SessionEnded{Reason}` | `SessionEnd`; after `EndExit` the UI quits |
| `TurnDone{Stop, Usage, Err}` | end of `Prompt` |

`Body` is closed the same way as `llm.StreamEvent`: an unexported method that does work,
`apply(*state, from) error`. Each body records itself in the live state before the UI sees it.

### Live state

What is happening now: open tool calls with their status, the plan, running subagents, and the
usage since the session was opened. It serves two things:

- **End of a turn:** a call still open when the run ends (cancelled, stopped by a hook, a stream
  that failed) gets a `ToolFinished` with `cancelled` or `skipped`, so a UI never keeps a spinner
  for a call that will not finish.
- **A UI that connects mid-session** (ACP `session/load`, a TUI reattaching) starts from `State()`.

An update that does not fit the state (a finish for a call that never started) is a bug in loom's
events: it is logged as a warning and still sent.

## Slash commands

A prompt whose first word is `/name` (lowercase letters, digits, `-`) runs a command in place of a
model turn. `/usr/bin is empty` is a prompt: its first word has a second slash.

| Command | Does |
|---|---|
| `/help` | a `Notice` with the commands |
| `/compact` | compacts the history now: every strategy runs, not only until it is under the target (`Conversation.Compact` → `Agent.Compact`, trigger `manual`); `Compacted` shows the sizes, a `Notice` says when there is nothing to compact or compaction is off |
| `/clear` | ends the session (`clear`) and builds a fresh Conversation with the same options: new ID, empty history, live state reset |
| `/exit` | ends the session (`prompt_input_exit`); later prompts get `ErrClosed` |

- An unknown command is a `Notice` ("unknown command /x; /help lists them"); its text is not sent
  to the model.
- Each command reports through updates and ends with `TurnDone`, like a turn.
- Where: `internal/frontend/command.go` (the `Command` type, parsing, dispatch) and `commands.go`
  (the built-ins). A new command is one `Command` value in `builtin()`. `Command.run` is
  unexported, so a UI lists commands but runs them only through `Prompt`.
- Later: user commands from `.loom/commands/*.md` (a saved prompt sent to the model), loaded into
  the same list the way skills are; ACP sends the list as `available_commands_update`.

## Build order

1. Display events, fired by the loop. **Done.**
2. `internal/frontend`: `Session`, updates, live state; tests with `llmtest.Replay` and a
   recording `UI`. **Done.**
3. `-p` through `frontend` with a printer `UI`: text streams to stdout. One code path.
4. Approvals: `UI.Approve(ctx, Approval) (Answer, error)`, asked when no hook decided; one at a
   time; cancel ends the wait as a deny. Answers: allow / deny, once / always.
5. Phase 2: `internal/acp` as a `UI` on top of `frontend`.

## Known gaps

- A `SubagentStop` hook can block the stop and keep the subagent working; the frontend has already
  sent `SubagentStopped`. Observers do not see outcomes.
- `Usage` updates and `State().Usage` count model calls since `Open`, not the session's earlier
  runs when it was resumed.
- Tool input is shown whole at `ToolStarted`, not as it streams.
