# Hooks

How loom's hooks work, and what a hook may answer on each event. Code: `internal/hook`,
`internal/hook/command`, events in `internal/event`.

## The flow

1. Something happens and loom fires an **event**: the model wants to run a tool (`PreToolUse`), a
   session starts (`SessionStart`), a skill is loaded (`SkillLoaded`).
2. The **pipeline** runs every hook registered for that event, in registration order.
3. Each hook returns an **`Outcome`**: what it wants loom to do.
4. The pipeline drops the parts of the outcome the event does not accept (see the table), merges
   what is left, and returns one outcome to the code that fired the event.
5. That code acts on it: runs the tool or not, adds text to the prompt, keeps the run going.

There is one pipeline per app. The order is fixed in `internal/app/build.go`:

```
trace (every event)  →  command hooks from config  →  permission policy  →  todo reminder
```

The trace comes first so it records everything. The permission policy comes after the command
hooks so it judges the tool input they may have rewritten.

## Outcome: what a hook can answer

| Field | Meaning |
|---|---|
| `AdditionalContext` | Text for the model (see below where it goes) |
| `Decision` | `allow`, `ask` or `deny` |
| `UpdatedInput` | New tool input, used instead of the model's |
| `UpdatedOutput` | New tool output, used instead of the tool's |
| `Block` | Do not proceed; on `Stop`, keep the run going instead |
| `Reason` | Why; shown to the model or in the logs |
| `Halt` | End the whole run now |

## Answers: what each event accepts

`Answer` names the kinds of answers; `answers` (in `internal/hook/answer.go`) says which kinds each
event accepts. `Reason` and `Halt` are accepted everywhere.

| Event | Accepts | What loom does with it |
|---|---|---|
| `SessionStart` | context | appended to the system prompt |
| `UserPromptSubmit` | context, block | context goes with the prompt as a note; block rejects the prompt |
| `ModelRequest` | context | added to the last user message before the call |
| `SubagentStart` | context | a note after the subagent's task |
| `PreToolUse` | decision, rewrite input | deny → the tool does not run; ask → `PermissionRequest` |
| `PermissionRequest` | decision | who approves when the policy asks |
| `PostToolUse` | context, rewrite output, block | context and block reason go after the output |
| `PostToolUseFailure` | context | added after the error |
| `PostToolBatch` | context | added after the turn's tool results |
| `PreCompact` | context | instructions for the summary, if one is written |
| `Stop`, `SubagentStop` | block | the reason goes to the model and it keeps working |
| everything else | — | a notice: hooks can watch it and halt, nothing more |

A hook that answers something its event does not accept is not an error. The field is cleared and
a warning is logged, so a script author can see why nothing happened:

```
level=WARN msg="hook answer ignored" event=SessionStart field=decision
```

Before this table, such answers were ignored silently, and a `deny` on a notice even stopped the
hooks after it from running.

## Merging

When several hooks answer the same event:

- **decision**: the strictest wins: deny > ask > allow;
- **reasons** are joined with `; `, **context** with a new line. Context that becomes its own text
  block is a system note (`hook_context`, see `docs/messages.md`): the model gets it in a
  `<loom-note>` tag, and memory extraction skips it;
- **input / output**: the last rewrite wins, and later hooks see the rewritten value;
- **block** and **halt**: any hook can set them;
- a **deny** or **halt** stops the chain: later hooks do not run.

## Fire and Notify

- `Fire(ctx, e)` returns the merged outcome and any hook error. The caller uses the outcome.
- `Notify(ctx, e)` is for events whose outcome nobody reads (`SkillLoaded`, `SkillUsed`,
  `InstructionsLoaded`, `TodoUpdated`, `SessionEnd`). A failing hook is logged, never returned,
  so it cannot stop loom.

## Example 1: a Go hook

Registered in code, typed by the event it handles:

```go
hook.On(hooks, func(ctx context.Context, e event.PreToolUse) (hook.Outcome, error) {
	if e.ToolName == tool.Bash && strings.Contains(string(e.ToolInput), "rm -rf") {
		return hook.Outcome{Decision: hook.Deny, Reason: "no recursive deletes"}, nil
	}
	return hook.Outcome{}, nil
})
```

The permission policy and the todo reminder are hooks like this one.

## Example 2: a command hook

Scripts from the config, in Claude Code's format, so existing Claude Code hooks work unchanged.
In `.loom/config.json`:

```json
{
  "hooks": {
    "PostToolUse": [
      { "matcher": "Edit|Write", "hooks": [{ "type": "command", "command": "./scripts/lint.sh" }] }
    ],
    "SessionStart": [
      { "hooks": [{ "type": "command", "command": "git status --short" }] }
    ]
  }
}
```

The script gets the event as JSON on stdin (`hook_event_name`, `session_id`, `cwd`, `tool_name`,
`tool_input`, ...). The matcher is a regexp on the tool name, used only for tool events.

What the script returns:

| Script does | Result |
|---|---|
| exit 0, prints nothing | no answer |
| exit 0, prints plain text | context on `SessionStart` and `UserPromptSubmit`; ignored elsewhere |
| exit 0, prints JSON | parsed as Claude Code's output (below) |
| exit 2 | deny on `PreToolUse`/`PermissionRequest`, block elsewhere; stderr is the reason |
| any other exit, crash, timeout, bad JSON | logged as a warning; the script is skipped |

So `git status --short` above puts the changed files into the system prompt at session start.

The JSON a script can print:

```json
{
  "continue": false,
  "stopReason": "tests are red",
  "decision": "block",
  "reason": "fix the lint errors first",
  "hookSpecificOutput": {
    "permissionDecision": "deny",
    "permissionDecisionReason": "not in this repo",
    "updatedInput": { "command": "ls -la" },
    "additionalContext": "lint: unused variable x on line 12",
    "updatedToolOutput": "trimmed output"
  }
}
```

`continue: false` is halt, `decision: "block"` is block, `permissionDecision` is the decision.
Every field is optional, and each still has to be accepted by the event: `updatedInput` from a
`PostToolUse` script, for example, is dropped with a warning.

A lint hook that sends errors back to the model after every edit:

```sh
#!/bin/sh
out=$(golangci-lint run ./... 2>&1) && exit 0
jq -n --arg ctx "$out" '{hookSpecificOutput: {additionalContext: $ctx}}'
```

## Adding an event

1. Add the type and its `Name()` in `internal/event/event.go`, and list it in the catalog.
2. If hooks may answer it, add one line to `answers` in `internal/hook/answer.go`.
3. Fire it with `Fire` when the caller reads the outcome, with `Notify` when it does not.
