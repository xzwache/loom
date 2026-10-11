# loom: feature candidates

Options for the coding agent, collected on 2026-10-11. Nothing here is decided.
None of it goes in before the basic loop works (one `bash` tool, loop until no tool call).

Items marked ★ are the ones rated highest for the effort.

## 1. Professional baseline

Expected in a serious harness, often missing in hobby ones.

- [ ] **Hard budgets**: limits on turns, tokens, cost and wall-clock time, with a clean stop and a reason.
  Risk: low; easy to do, easy to forget.
- [ ] **Full run trace**: every request, response and tool call written as JSONL per run.
  Risk: traces can capture secrets from tool output.
- [ ] **Fake model for tests**: a scripted stand-in for the API, so the loop is unit-tested without spending tokens.
  Risk: scripts drift from real model behavior.
- [ ] **Sandboxed execution**: commands run in a container, with permission rules for what is allowed.
  Risk: Docker adds setup friction for users.
- [ ] **Headless mode**: a flag that runs one task and prints JSON, with no interaction.
  Risk: none; needed for any measurement.
- [ ] **Usage report per run**: tokens, cache hits and cost, printed at the end.
  Risk: prices change, so keep them in config.

## 2. Stand-out features

Rare in agent repos.

- [ ] ★ **Deterministic replay**: re-run a recorded trace against changed harness code without calling the model.
  Risk: only works while the change does not alter what the model would have seen.
- [ ] ★ **Built-in ablations**: every feature behind a flag, and a `bench` command that runs a task set and prints the comparison table.
  Risk: needs a task set; results are noisy on few tasks.
- [ ] **Context inspector**: a view of `messages[]` per turn with each block's token share, plus manual pruning.
  Risk: UI work can eat the project.
- [ ] **File checkpoints per turn**: snapshot the working tree each turn, and rewind files and conversation together.
  Risk: Claude Code has rewind already, so it must go further to count. Same problem area as claude-time-machine.
- [ ] **Pluggable compaction**: several strategies behind one interface, each measured.
  Risk: hard to measure without long tasks.
- [ ] **Crash resume**: run state persisted, so a killed process continues where it stopped.
  Risk: a half-finished tool call may have side effects that cannot be detected.
- [ ] **Independent verifier**: a separate context checks the work before the agent may say "done".
  Risk: roughly doubles cost on short tasks.
- [ ] **Injection test suite**: fixture repos with instructions planted in files, and a count of how often the agent obeys them.
  Risk: results depend heavily on the model.

## 3. Ecosystem compatibility

The agent works with things people already have.

- [ ] **MCP client**: connect to existing MCP servers so their tools appear in the agent.
  Risk: expected by now, so it earns little credit alone.
- [ ] **Standard skills and project files**: load `SKILL.md` folders and `AGENTS.md` as other agents do.
  Risk: low; mostly file parsing.
- [ ] **Hooks**: user scripts that run before and after tool calls and can rewrite or block them.
  Risk: a hook API is easy to design badly and hard to change later.
- [ ] **Editor protocol**: speak the Agent Client Protocol so editors such as Zed can drive the agent.
  Risk: the spec is young and may shift. (Protocol name not verified.)

## 4. Reliability

Where real agents fail most.

- [ ] ★ **Edit tool with failure stats**: several edit formats (exact replace, diff, whole file), with a measured failure rate for each.
  Risk: tedious; results vary by model.
- [ ] ★ **Stuck detection**: notice repeated identical tool calls or no progress, then intervene or stop.
  Risk: false positives interrupt legitimate retries.
- [ ] **Diagnostics after edits**: run the linter or type checker automatically after each edit and feed errors back.
  Risk: slow on big projects; per-language setup.
- [ ] **Interrupt and steer**: Ctrl-C stops mid-turn without corrupting history, and a correction can be typed.
  Risk: tool-use pairs must stay valid after the cut.
- [ ] **Background commands**: long builds and tests run without blocking the loop, with a notification on finish.
  Risk: concurrency bugs.

## 5. Code understanding

- [ ] **Repo map**: a compact symbol index built with tree-sitter and given to the model up front, the way Aider does it.
  Risk: may not beat plain `grep` on small repos, so measure it.
- [ ] **Parallel read-only tools**: run several `read` and `grep` calls from one response concurrently.
  Risk: small gain unless tasks are search-heavy.

## 6. Measurement and models

- [ ] ★ **Terminal-Bench adapter**: plug the agent into the public benchmark and get a score comparable with other agents.
  Risk: the score will be modest, and runs cost real money.
- [ ] **Model matrix**: the same tasks across several models, including a local one.
  Risk: cost, and per-model prompt quirks.
- [ ] **Cheap-model routing**: subagents and exploration go to a cheaper model, with the cost and quality difference measured.
  Risk: quality drops can be subtle.
- [ ] **Cache-aware context**: keep the prompt prefix stable on purpose and report the cache hit rate per run.
  Risk: constrains how compaction and memory are done.
- [ ] **Session forking**: branch a run at any turn to try two approaches and compare them.
  Risk: files need branching too, not only the conversation.

## From the course (s01 to s10), as the base to build on

- [ ] Agent loop with tool dispatch
- [ ] Base tools: `bash`, `read`, `write`, `edit`, `glob`
- [ ] Todo list with reminder
- [ ] Subagents with a fresh context
- [ ] Skills catalog and on-demand loading
- [ ] Context compaction pipeline
- [ ] Memory across sessions
- [ ] Persisted task system with dependencies
