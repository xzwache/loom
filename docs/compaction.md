# Compaction (step 9)

How loom keeps a long run inside the model's context window. Status: built (step 9); code in
`internal/compact`, wired in `internal/agent/compact.go`.

## The problem

Every turn sends the whole history. Tool output (file reads, test logs, command output) is most
of it. On a long task the history reaches the context window, the API answers with
`model_context_window_exceeded`, and the run stops with `context_full`. Long before that, every
turn gets slower and more expensive, and the model gets worse at using what is in the middle.

## What others do

| | When | How |
|---|---|---|
| Claude Code | automatically near the limit (~95%, configurable), or `/compact [instructions]` | the model writes a structured summary (request, key decisions, files, errors, pending work, next step); the history is replaced by it; `PreCompact` hook with `manual` / `auto` |
| Codex CLI | at a token limit set per model | the model writes a hand-off summary; a new context starts from it |
| OpenHands | a "condenser" runs before each call | pluggable: mask old tool output, keep the first and recent events, LLM summary |
| Anthropic API | server-side, by thresholds you set | context editing (`clear_tool_uses_20250919`, `clear_thinking_20251015`) and compaction (`compact_20260112`), both beta |

Two lessons hold across them:

- **Clearing old tool output is cheap and works well.** It needs no model call, keeps every
  message and the reasoning, and removes most of the tokens. Research on coding agents (2025)
  found it close to LLM summaries in quality for much less cost.
- **A summary is the last resort.** It costs a model call and loses detail, but it is the only
  thing that works when the conversation itself is too long.

## Approach for loom

A **chain of strategies**, cheapest first. When the context passes a threshold, the chain runs
each strategy in order and stops as soon as the history is small enough. This is the
"compact chain with reports" from the plan.

1. **Offload old tool output** (`OffloadOutputs`). Tool results older than the last `keep_turns`
   turns are saved to `.loom/outputs/<session-id>/<tool-use-id>.txt` in the workspace (a
   subagent's under `<session-id>/<agent-id>/`), and their content becomes a marker with the size
   and the path: `[output offloaded to save context: 840 lines, 31200 bytes, in
   .loom/outputs/…/toolu_01.txt; read it if you need it]`. The model reads it back with `read`
   (allowed by default, paged with `offset`/`limit`) instead of running the tool again. Every
   `tool_use` keeps its `tool_result`, so the history stays valid.
   - Output that cannot be saved **stays in the history**: nothing is removed without a copy on
     disk. The failures are counted in `PostCompact` (`failed`, `error`) and logged as a warning.
   - An offload that frees less than `min_offload_tokens` is skipped: the summary that has to
     follow would replace it anyway, so writing the files would be wasted.
   - Results shorter than their marker, or already offloaded, stay as they are, so a second pass
     never overwrites a saved file with its marker.
   - Output may hold secrets (someone ran `cat .env`). loom's API key is redacted before writing;
     files are 0600 in 0700 folders; `.loom/outputs/.gitignore` (`*`) keeps them out of git in any
     repository; folders of sessions older than `outputs_retention` (7 days) are deleted at
     startup. Other secrets in output cannot be detected reliably and are kept as they are.
2. **Summarize.** Send the history, as offloading left it, to the model as a plain-text transcript
   (tool results cut to 2,000 bytes each; the whole transcript capped at half the context window
   by cutting the middle, so the task at the start and the latest state at the end stay) with a summary prompt (`internal/compact/summary.md`). The new history is one
   user message: the summary and "continue the task". Nothing older is kept word for word, so no
   tool pairs can break and thinking blocks stay valid. A transcript instead of the messages
   themselves means the request needs no tool definitions.

The chain compacts from `threshold` down to `target` (80% → 50% by default). The gap matters: a
history compacted only just under the threshold would compact again a few turns later, and each
compaction costs a prompt-cache miss.

### When it runs

- **Proactive**: before each model call, in the agent loop. The size is the last response's
  `input + cache read + cache write + output` tokens, counted by the API, plus an estimate of the
  messages added since (the tool results of that turn, often the largest part). When that passes
  `threshold × context_window`, the chain runs.
- **Reactive**: when a response still ends with `context_full`, drop that cut-off response,
  compact once and send the turn again. A second `context_full` ends the run as before.
- **Manual**: `/compact [instructions]` in the TUI later; nothing in headless mode for now.

### Events and reports

- `PreCompact {trigger: auto | reactive | manual, tokens}` fires before the chain. It accepts
  **context**: hooks can add instructions for the summary ("keep the list of failing tests").
  One line in `answers`.
- `PostCompact {trigger, strategy, tokens_before, tokens_after}` is a notice, one per strategy
  that changed the history. It goes to the trace, so every compaction is visible.
- Sizes after a strategy are estimates: the tokens the API counted, minus an estimate (4 bytes
  per token) of what the strategy removed. The next response gives the real number again.
- A failed compaction (say, the summary call fails) is logged; the run goes on with what the
  strategies before it achieved.

### Costs to keep in mind

- **Prompt cache.** Any change to old messages invalidates the cache from that point on. That is
  why offloading happens in one batch when the threshold is passed, not a little every turn. The
  system prompt and tools stay cached.
- **The summary call** uses tokens. They count in the run's usage and budget, as subagent
  tokens now do.
- **Subagents** run the same chain with their own context. Most are short and never reach it.

## Code

New package `internal/compact`:

```
compact.go      Strategy, Chain, Report, Trigger
offload.go      OffloadOutputs
archive.go      Archive: offloaded output on disk, .gitignore, pruning
summarize.go    Summarize (uses llm.Model)
size.go         ContextSize from usage; Estimate for added or removed messages
summary.md      the summary prompt, embedded
```

The core types, nothing more:

```go
// Strategy makes a history smaller. It returns the history unchanged when it cannot.
type Strategy interface {
	Name() string
	Compact(ctx context.Context, h []llm.Message) ([]llm.Message, error)
}

// Chain runs strategies in order until the history fits.
type Chain struct {
	Strategies []Strategy
	Target     int // tokens to get under
}

type Report struct {
	Strategy     string
	TokensBefore int
	TokensAfter  int
}

func (c Chain) Run(ctx context.Context, h []llm.Message, size int) ([]llm.Message, []Report, error)
```

Changes elsewhere:

| Where | Change |
|---|---|
| `internal/agent/loop.go` | one step in `turn`: `compactIfNeeded` before `send`; on `context_full`, compact and retry once |
| `internal/agent/agent.go` | `Agent.Compactor` field (nil means off) |
| `internal/config` | `compaction` settings (below), validated |
| `internal/event` | trigger constants; `strategy` on `PostCompact` |
| `internal/hook/answer.go` | `PreCompact: AddsContext` |
| `internal/app/build.go` | build the chain from config, for the agent and the subagent template |
| `internal/session` | nothing: the compacted history is saved like any other |

Config:

```json
{
  "compaction": {
    "context_window": 200000,
    "threshold": 0.8,
    "target": 0.5,
    "keep_turns": 5,
    "min_offload_tokens": 2000,
    "summary_model": "",
    "outputs_retention": "168h"
  }
}
```

`summary_model` empty means the run's own model. `threshold: 0` turns compaction off.

## Tests

- Offloading keeps every tool pair and the last `keep_turns` turns untouched.
- The chain stops after offloading when that is enough, and summarizes when it is not.
- A long scripted run stays under the limit; `PreCompact` and `PostCompact` are in the trace.
- A `context_full` response compacts once and retries; a second one ends the run.
- Subagent and summary tokens count in the run's usage.

## Decisions

1. Threshold 80%, target 50%, both configurable.
2. `context_window` from config (default 200k); a table of known models comes with the provider
   abstraction.
3. The trace is the record of what was compacted; no second session file.
4. Server-side compaction (`compact_20260112`) may come later, behind the `llm` interface.

## Not done yet

- The summary text itself is not in the trace (only the sizes).
- The todo list is not added to the summary message; the todo reminder brings it back.
- `/compact` waits for the TUI.
