# Approvals

When the permission policy says `ask` for a tool call and no `PermissionRequest` hook decides, loom
asks the user. Without someone to ask (headless `-p`), the call is denied, as before.
Status: built 2026-11-05.

```
PreToolUse hooks → policy: ask → PermissionRequest hooks → no decision → Approver → allow / deny
```

## API

```go
// agent
type Approver func(ctx context.Context, a Approval) (Answer, error)

type Approval struct {
	Call   event.ToolCall // with the input PreToolUse hooks rewrote
	Agent  event.Agent    // zero for the main agent
	Reason string         // why the policy asks
	Always []string       // the rules AllowAlways adds; empty: do not offer it
}

// Answers: AllowOnce, AllowAlways, Deny
```

- `agent.Agent.Approve` is shared with subagents, like the hooks.
- `app.Options.Approve` takes the UI's approver. The app wraps it: it fills `Always` from the
  policy, and on `AllowAlways` adds those rules to the session.
- `frontend`: a UI that also implements `frontend.Approver` is asked; others are not, and their
  calls are denied.

## Answers

| Case | Result |
|---|---|
| allow once | the call runs |
| allow always | `Always` becomes session allow rules (`Policy.AllowForSession`), then the call runs. In memory only: gone when the session ends |
| deny | the model gets `denied: the user denied it`; `PermissionDenied` fires |
| prompt cancelled while asking | denied (`not approved: the run was stopped`); the turn ends `cancelled` |
| the approver fails (UI gone) | denied with the error, logged |
| allow always when `Always` is empty | an error: the UI offered what it should not have |
| several calls ask at once (parallel subagents) | `frontend` asks one at a time; the tool shows `awaiting_approval` in `State` |

## What "always" allows

`Policy.SessionRules(tool, input)` builds the rules, so the UI can show them before the user picks
("Always allow `Bash(make install *)` this session"):

| Tool | Rule | Example |
|---|---|---|
| `bash` | each command that asks: its first word, plus the second when it is a subcommand (lowercase letters, digits, `-`) | `make install` → `Bash(make install *)`; `curl -s x` → `Bash(curl *)`; in `go build . && make test` only `make test` asks |
| `read`, `edit`, `write` | the file's directory; a file at the workspace root by itself | `Edit(/internal/llm/**)`, `Write(/main.go)` |
| others (MCP, unknown) | the tool | `mcp__github__create_issue` |

As in Claude Code, an `Edit` rule also covers `write`.

No rule is offered when no allow rule could make the call run:

- it is denied (a deny rule, the floor, plan mode);
- an ask rule or the mode asks: those ask every time by design;
- a part cannot be named by a rule: an expansion (`$TOOL run`), an assignment (`FOO=1 make`), a
  line that does not parse, glob characters in a path;
- a symlink whose target is outside the rule's directory.

## Later

- Save "always" to the project settings (`.loom/settings.json`), as Claude Code does.
- `-p` answers from a flag.
- "Deny always" (ACP's `reject_always`): left out, a deny rule from one click is easy to set by
  mistake.
