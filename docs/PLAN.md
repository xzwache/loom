# loom core plan (s01–s10)

Goal: a working core agent with the architecture every later feature plugs into.
Each step is one PR, small enough to review, and ends with a short summary.

## Architecture

```
cmd/loom            flags, config, wiring, run() -> exit code
internal/llm        loom message types, Provider interface, anthropic impl
internal/llm/llmtest scripted fake provider for hermetic tests
internal/agent      the loop: stop_reason switch, budgets, dispatch, Agent spec (also used for subagents)
internal/tool       Tool interface, Registry, builtin/{bash,read,write,edit,glob}
internal/event      typed lifecycle events, versioned (see Events below)
internal/hook       Pipeline over events: allow / deny / modify / continue decisions
internal/permission rules (allow/ask/deny by tool + pattern), modes, shell parsing, registered as a PreToolUse hook
internal/prompt     system prompt built from ordered sections (stable prefix)
internal/todo       todo tool + reminder (via a hook)
internal/skill      SKILL.md discovery, catalog section, load_skill tool
internal/compact    Compactor chain, each stage returns a report
internal/memory     Store interface + file store, memory tools
internal/task       task graph (blockedBy, cycle check) + file store, task tools
internal/trace      JSONL run trace (an event subscriber)
internal/config     typed config, layered loading
internal/session    Session + History + Store (memory, file): resume and continue
```

The two seams that the later features depend on:
1. **Events + hooks**: permissions, todo reminders, tracing, stuck detection, the verifier,
   and user hooks all attach here instead of to the loop.
2. **Agent spec**: an `Agent` is config (model, prompt sections, tool subset, budget).
   A subagent is the same type with a fresh history, so cheap-model routing and forking come almost for free.

## Steps

| # | Step | Course | Done when |
|---|---|---|---|
| 0 | Scaffold: go.mod, Makefile, golangci config, CI, config package | — | `make check` green in CI |
| 1 | `llm`: message types, Provider, Anthropic impl, fake provider, retries | s01 | Fake-provider tests; one integration test with a real key |
| 2 | `agent` loop with the `bash` tool, stop_reason handling, budgets, headless mode | s01 | `loom -p "..."` solves a small task |
| 3 | `tool` registry + read/write/edit/glob, output truncation, path safety | s02 | Each tool table-tested |
| 4 | `event` catalog + `hook` chain, `trace` as a subscriber | s04 | Trace JSONL shows every step |
| 4b | Command hooks compatible with Claude Code (JSON on stdin/stdout, exit 2 blocks) | — | An existing Claude Code hook script works unchanged |
| 5 | `permission`: Claude Code-style rules and modes as a PreToolUse hook (see `docs/research/permissions.md`); the interactive ask prompt comes with interactive mode, as a PermissionRequest hook | s03 | Denied call returns an `is_error` result, and the denial is in the trace |
| 6 | `prompt` sections + `todo` tool and reminder | s05 | Reminder fires after N turns without a todo update |
| 7 | Subagents: `agent` tool spawning an Agent with a tool subset and budget | s06 | Parent receives one summary; no recursion |
| 8 | `skill` loading | s07 | Catalog in prompt, body loaded on demand |
| 9 | `compact` chain with reports | s08 | Long-run test keeps context under the limit; reports in the trace |
| 10 | `memory` store + tools | s09 | Facts survive a restart |
| 11 | `task` graph + tools | s10 | Dependency order and cycle rejection tested |

Steps 4–5 come before 6–11 on purpose: everything after them is built on hooks.

## Events

The whole catalog is defined in step 4 (`event.SchemaVersion` = 1); each event is fired once
the feature behind it exists. Names and JSON fields follow Claude Code hooks where one exists,
so the step 4b adapter maps them one to one. Loom-only events are marked *.

| Group | Events | Fired since |
|---|---|---|
| Session | `SessionStart`, `SessionEnd` | 4 |
| Turn | `UserPromptSubmit`, `Stop`, `StopFailure` | 4 |
| Model | `ModelRequest`*, `ModelResponse`* | 4 |
| Tools | `PreToolUse`, `PostToolUse`, `PostToolUseFailure`, `PostToolBatch` | 4 |
| Budget | `BudgetExceeded`* | 4 |
| Permissions | `PermissionDenied` (4), `PermissionRequest` (5) | 4–5 |
| Prompt, todo | `InstructionsLoaded`, `TodoUpdated`* | 6 |
| Subagents | `SubagentStart`, `SubagentStop` | 7 |
| Skills | `SkillLoaded`*, `SkillUsed`* | 8 |
| Compaction | `PreCompact`, `PostCompact` | 9 |
| Memory | `MemoryRead`*, `MemoryWrite`* | 10 |
| Tasks | `TaskCreated`, `TaskCompleted` | 11 |
| Display | `Notification`, `MessageDisplay` | interactive mode |
| Environment | `ConfigChange`, `FileChanged`, `CwdChanged` | later |
| Model switch | `PreModelSwitch`, `PostModelSwitch` | later |

A hook returns an `Outcome`: a decision (allow / ask / deny), a rewritten tool input or output,
additional context for the model, `Block` (on `Stop`: keep going) or `Halt` (end the run).
Hooks run in registration order and later hooks see earlier rewrites; deny and halt stop the
chain. A failing hook is logged and skipped, except on `PreToolUse`, where it denies the call.

## Naming

"Task" means only the step 11 task graph. A `-p` run is a headless run (`cmd/loom/headless.go`),
and the subagent tool in step 7 is called `agent`.

## Command hooks (step 4b)

`"hooks"` in any config file uses Claude Code's format and adds to the hooks of earlier files:

```json
{"hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "./check.sh", "timeout": 30}]}]}}
```

The script gets the event JSON on stdin with Claude Code's common fields (`hook_event_name`, `session_id`,
`cwd`, `permission_mode`), built-in tool names capitalized (`Bash`) and `CLAUDE_PROJECT_DIR` set; the API key is
removed from its environment. Exit 2 denies a tool call (or blocks, for other events) with stderr as the reason;
exit 0 parses `continue`, `decision`, `hookSpecificOutput.permissionDecision`, `updatedInput`,
`additionalContext` and `updatedToolOutput`; anything else is logged and ignored.

Command hooks run before the permission policy, so a hook can tighten a decision but never loosen it: a
script's `allow` does not skip a permission `ask`. To approve automatically, answer `PermissionRequest`.

## Upcoming design work

- **Sessions** (done in `feature/conversation`). `internal/session`: a `Session` (id, cwd, model, usage) with a
  `History` that keeps the message list valid to send, and a `Store` interface with memory and file
  implementations. Headless runs save to `$XDG_STATE_HOME/loom/sessions/<id>.json` (0600, atomic write);
  `-continue` and `-resume <id>` pick it up. Messages are stored as typed JSON blocks, thinking signatures
  included. Compaction (step 9) and subagents (step 7) build on it.
- **App wiring.** `cmd/loom/headless.go` builds the workspace, tools, hook chain (trace → command hooks →
  permission policy) and agent. That moves into one builder (e.g. `internal/app`) shared by headless mode and
  the TUI, so both get identical behavior.
- **TUI.** An interactive terminal UI (Bubble Tea): streaming output, tool calls and diffs, the permission prompt
  as a `PermissionRequest` hook (once / session / always), and slash commands.
- **Observability.** A sink abstraction fed by the hook chain (`OnAll`): the JSONL trace, structured logs,
  OpenTelemetry spans and metrics, and per-run cost and cache-hit reports.
- **Refactor `agent.loop`.** Split the ~100-line loop into small steps (budget check, model turn, stop-reason
  handling) so methods stay short.
