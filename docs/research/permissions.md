# Permissions in other agents

Research for step 5 (October 2026). Sources: Claude Code docs (code.claude.com/docs/en/permissions,
permission-modes, settings, sandboxing), pi (github.com/badlogic/pi-mono, `docs/security.md`,
`examples/extensions/`), Hermes Agent (github.com/NousResearch/hermes-agent, `tools/approval*.py`,
`website/docs/user-guide/security.md`).

## Comparison

| | Claude Code | pi | Hermes Agent |
|---|---|---|---|
| Model | Declarative rules + modes | None (YOLO) | Dangerous-pattern detection + approval |
| Default | Asks for writes and commands; reads are free | Never asks | An auxiliary LLM approves, denies or escalates to a human |
| Rules | `allow` / `ask` / `deny` lists, `Tool(specifier)`: `Bash(git push *)`, `Edit(src/**)`, `WebFetch(domain:x.com)` | Extensions on `tool_call` return `{block, reason}` | ~110 dangerous regexes, ~13 hardline regexes, `approvals.deny` globs |
| Precedence | deny > ask > allow, first match, specificity ignored | First blocking extension wins; a throwing extension blocks | Container skip > hardline floor > yolo > allowlist > patterns |
| Compound commands | Splits `&&`, `\|\|`, `;`, `\|`, `&`; checks each part; strips `timeout`, `nice`, `nohup`, `time`, `stdbuf`, `command`, bare `xargs` | — | Normalizes first (ANSI, NFKC, `r''m`, `$IFS`, wrappers, recursive `sh -c`) |
| Approval choices | Yes / yes, don't ask again (rule written to `.claude/settings.local.json`) / no | `ctx.ui.confirm` in an extension | once / session / always (`command_allowlist` in config) / deny |
| Headless | ask → deny, unless a `PermissionRequest` hook decides | — | `-q`, cron and API runs deny by default; 300s timeout fails closed |
| Modes | default, acceptEdits, plan, dontAsk, bypassPermissions, auto (classifier) | — | smart, manual, off (yolo) |
| File paths | gitignore-style: `//abs`, `~/`, `/settings-relative`, `./`; symlinks checked at both ends (deny if either matches, allow needs both) | Example: protect `.env`, `.git/`, `node_modules/` | Hard block on `~/.ssh`, `~/.aws`, `/etc/sudoers`, secret stores |
| Config layers | managed > CLI flags > local project > shared project > user | Project trust prompt before loading `.pi/*` | `~/.hermes/config.yaml` only |
| Sandbox | Separate OS sandbox for bash (sandbox-exec, bubblewrap) | "Run it in a container"; sandbox extension | Container backends skip checks except deny rules |

## Lessons

- **pi**: permission checks are not a security boundary. Once an agent can write and run code, only a
  container, VM or OS sandbox really contains it.
- **Claude Code**: the best UX. A small rule language, modes, and "don't ask again" that becomes a rule.
  Hook `allow` never overrides a deny rule.
- **Hermes**: a hardline floor that no mode overrides, and normalizing commands before matching.
  Its project-trust and context-file injection scanning are worth copying later.

## What loom does (step 5)

- Claude Code rules and precedence; tool names are case-insensitive, so `Bash(...)` and `bash(...)` both work.
  `Edit(...)` covers edit and write, `Read(...)` covers read.
- Compound commands are parsed with `mvdan.cc/sh` (a real shell parser); every simple command, including
  ones inside `$(...)`, subshells and functions, is checked on its own. Quoting tricks like `r''m` are
  normalized by reading literal word values.
- A built-in read-only command set is allowed without a rule, but only with static arguments, no file
  redirects and no paths outside the workspace.
- A hardline floor (rm -r of / or ~, mkfs, dd to a device, fork bomb, shutdown) is denied in every mode.
- Modes: `default`, `acceptEdits`, `plan`, `bypassPermissions`. No rule match means ask; in `-p` mode an ask
  becomes a deny unless a `PermissionRequest` hook allows it.
- Known gaps: no project trust (a cloned repo's `.loom/config.json` is trusted), no OS sandbox, no smart/auto
  approval. All three are on the feature list.

## Floor configuration

The built-in floor lives in `internal/permission/floor_rules.json` (embedded in the binary): blocked commands
in Bash rule syntax with a reason, protected paths for `rm -r`, and device prefixes. Config can only add to it,
under `permissions.floor`, so a cloned repo's config cannot remove protections. Deny rules also apply in every
mode, including `bypassPermissions`.
