# Memory

loom remembers facts across sessions: who you are, how you want it to work, and things about a
workspace that the code does not show. learn-claude-code s09. Built on `feature/memory`.

## Where it lives

Only in your user folder; a repository cannot plant memories.

```
~/.config/loom/memory/
  global/                facts for every workspace
    INDEX.md             one line per memory, rewritten by loom after each change
    short-commits.md
  projects/<key>/        facts for one workspace; key = folder name + hash of its path
    INDEX.md
    tests-need-db.md
```

One fact per file, plain markdown you can edit or delete by hand:

```markdown
---
name: tests-need-db
description: "integration tests need `make db` running first"
type: project
---
Run `make db` before `go test ./...`; otherwise tests time out on connect.
```

| Field | Rule |
|---|---|
| `name` | lowercase letters, digits, dashes; same as the file name; not `index` |
| `description` | one line, at most 200 characters: when the memory is relevant |
| `type` | `user` (who you are), `feedback` (how to work), `project` (goals, constraints), `reference` (where to find things) |
| body | at most 4000 characters |

A file that breaks these rules is left out and logged (`memory left out`), never silently fixed.

## How the model uses it

1. **Selection.** At startup the names and descriptions of the memories go into the system prompt
   (`# Memory`, after the project instructions), up to 10 KB; the model reads a memory's text with the
   `memory` tool when its description fits. The prompt says memory is background: the current request
   and project instructions win, and facts it names must be checked before use.
   Memories past the 10 KB are not dropped: the prompt says how many are unlisted, and before each run
   one cheap call picks at most 5 of them that fit the request; their names and descriptions go with
   the request as a second text block, a system note of kind `memory_hint` (see `docs/messages.md`),
   so the session file keeps your words apart and extraction skips it. No call happens while everything fits.
   **When the list is current:** the main prompt's list is built at startup and again after each
   compaction (`Agent.RebuildSystem`): changing the system prompt loses the cached prefix, which
   compaction has lost anyway. Between compactions, the result of every `write` and `delete` tells the
   model that it replaces the list. A subagent's list is built when it starts
   (`subagent.Tool.LiveContext`), so it sees memories saved earlier in the session.
2. **Saving by the model.** The `memory` tool: `list`, `read`, `write` (creates or replaces by scope +
   name), `delete`. The model saves when it learns something lasting, and at once when you say
   "remember" or "forget".
3. **Automatic extraction.** When a session ends (`App.End`), one model call reads this session's new
   messages and returns at most 5 memories to create or update, as JSON. It sees only what you and the
   agent said (origins `user` and `model`), never tool output or loom's system notes (hook context,
   memory hints, summaries), so text from files or web pages cannot become a memory on its own.
   `user` and `feedback` memories must come from what you said. Invalid candidates are rejected and
   logged; a failed call is logged and never fails the session.
4. **Consolidation.** After extraction, when there are at least `consolidate_at` memories and this
   session changed one, one call gets all of them and returns a plan: memories to write (merged or
   fixed) and memories to delete. loom checks the plan before touching a file: every write valid, every
   delete names an existing memory, nothing both written and deleted, and at most half of the
   memories deleted. It is applied all together: loom keeps a copy of every file it touches and puts
   them all back if one step fails. Unreadable memory files skip consolidation, since they would look
   missing to the model; so do memories over 100 KB in total.

## Subagents

Subagents see the same list and get a read-only `memory` tool (`list`, `read`), whatever their type
lists. They cannot write; they report what is worth keeping, and since that report reaches the main
agent's answer, extraction can save it.

## Settings

| Setting | Default | Meaning |
|---|---|---|
| `memory.enabled` | `true` | memory list in the prompt, the `memory` tool |
| `memory.auto_extract` | `true` | the extraction call at session end |
| `memory.consolidate_at` | `30` | memories needed before consolidation; `0` turns it off |
| `memory.model` | subagent model, then main model | model for extraction, selection and consolidation |

Permissions: `memory` changes only loom's own state, so it is allowed in every mode, like `todo`. A
deny rule turns it off for the model: `{"permissions": {"deny": ["memory"]}}`.

## Tokens

- Selection runs for the run it prepares: its tokens are part of the run's usage and count toward
  `max_run_tokens` (`Agent.RunAfter`).
- Extraction and consolidation run after the run, in `App.End`: their tokens are added to the
  session's usage, saved with it, and included in the `-json` output's `usage`. After a run stopped
  by `max_run_tokens` both are skipped, and the log says so.

## Events

| Event | Fields | When |
|---|---|---|
| `MemoryRead` | `path` | the tool read a memory |
| `MemoryWrite` | `path`, `action` (`create`, `update`, `delete`), `by` (`model`, `extraction`, `consolidation`) | a memory changed |

## Code

| File | What |
|---|---|
| `internal/memory/memory.go` | `Memory`, validation, `Store` (list, read, write, delete, `Apply` with restore, index), `ProjectKey` |
| `internal/memory/tool.go` | the `memory` tool, read-only for subagents |
| `internal/memory/prompt.go` | `Section`, `Fit` (what fits the prompt), `Catalog`, `Hint` |
| `internal/memory/helper.go` | `Helper`: `Extract`, `Select`, `Consolidate` (plan checks); prompts in `extract.md`, `select.md`, `consolidate.md` |
| `internal/app/memory.go` | `addMemory` build step; selection in `Run`; extraction and consolidation in `End` |

## Later

- Rules that deny only writes (`memory(write)`); today a rule covers the whole tool.
- TUI: `/memory` to list and edit, and showing what extraction saved at the end of a session.
