# Messages and text origins

The API knows two roles, `user` and `assistant`, and takes one user message per turn. So one user
message often holds several things: what you typed, tool results, and notes loom adds. To keep
them apart, every text block says who wrote it.

```go
type Text struct {
	Text   string
	Origin Origin // user, model or system
	Kind   Kind   // only for system: what the note is for
}
```

| Origin | Who | Made with |
|---|---|---|
| `user` | the person (for a subagent: the task its parent gave it) | `llm.UserText` |
| `model` | the model's replies | `llm.ModelText` (set where responses are read, `anthropic.go`) |
| `system` | loom: never the user's words | `llm.SystemText(kind, text)` |

| Kind | What | Added by |
|---|---|---|
| `hook_context` | `additionalContext` from hooks | the agent loop, the subagent tool |
| `memory_hint` | memories picked for a request | `Conversation.Run` (see `docs/memory.md`) |
| `summary` | the compaction summary that replaces the history | `compact.Summarize` |

## Rules

- **Every text has an origin.** `History.Validate` (on every load and save) rejects a text with
  no origin, a system text without a kind, model text in a user message, and anything but model
  text in an assistant message. So a forgotten label fails, it never passes as user text.
- **The model sees the difference.** A system text is sent wrapped:
  `<loom-note kind="memory_hint">…</loom-note>` (`Text.Sent`), so a note never reads as the user.
- **Sessions keep it.** Saved as `"origin"` and `"kind"` on each text block. Sessions saved before
  origins existed get theirs from the role on load (user → `user`, assistant → `model`); their
  old notes can no longer be told apart.

## Using it

- `Message.Texts(origins...)` returns a message's texts from those origins: memory extraction
  reads only `user` and `model` text.
- The compaction transcript labels system texts `[loom note: <kind>]`.
- Later: the TUI shows user and model text and hides or greys notes; old reminders and hints can be
  dropped before compaction by kind.
