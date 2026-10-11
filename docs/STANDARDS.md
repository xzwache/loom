# loom engineering standards

How code is written, tested, reviewed and committed in this repository.
Where this document is silent, follow the sources below, in this order.

| Priority | Source |
|---|---|
| 1 | This document |
| 2 | [Google Go Style Guide](https://google.github.io/styleguide/go/guide) (canonical), then its [Decisions](https://google.github.io/styleguide/go/decisions) and [Best Practices](https://google.github.io/styleguide/go/best-practices) |
| 3 | [Effective Go](https://go.dev/doc/effective_go) and [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments) |
| 4 | [Uber Go Style Guide](https://github.com/uber-go/guide/blob/master/style.md) |
| 5 | [Google Engineering Practices: code review](https://google.github.io/eng-practices/) |

Google's order of priorities for readable code applies to every decision:
**clarity > simplicity > concision > maintainability > consistency**.

---

## 1. Toolchain

- Go version: the one pinned in `go.mod` (currently 1.26). No language features newer than that.
- Formatting: `gofmt` (via `gofumpt`) and `goimports`. Unformatted code does not get merged.
- Linting: `golangci-lint` with the repository config. CI fails on any finding.
  A `//nolint:<linter> // reason` needs the specific linter and a reason.
- `go vet` and `go test -race ./...` pass before every commit.
- All of these run through `make check`. CI runs the same target.

## 2. Repository layout

```
cmd/loom/        package main only: flags, wiring, run() -> exit code
internal/<pkg>/  everything else; nothing is importable from outside
docs/            design notes, plans, this file
testdata/        fixtures (ignored by the go tool)
```

- Everything lives in `internal/` until there is a real reason to publish an API.
- One package = one concept (`agent`, `llm`, `tool`, `hook`, `permission`, ...).
  No `util`, `common`, `helpers` or `misc` packages.
- Package names: short, lowercase, singular, no underscores. Call sites read well: `tool.Registry`, not `tool.ToolRegistry`.
- Dependencies point inward: `cmd` wires everything; `agent` depends on interfaces, not on concrete providers.
  Import cycles are design bugs, so don't fix them with a shared "types" package.

## 3. Naming

- Follow Go conventions: `MixedCaps`, initialisms keep their case (`ID`, `URL`, `HTTPClient`, `toolUseID`).
- No `Get` prefix on accessors: `Name()`, not `GetName()`.
- Don't repeat the package or receiver: `compact.Chain`, `(*Registry).Register`, not `compact.CompactChain`.
- Name length tracks scope: `i` in a 3-line loop, a descriptive name for a package-level identifier.
- Error variables: `ErrSomething`. Error types: `SomethingError`.
- Enums start at `iota + 1` unless the zero value is a meaningful default.

## 4. Package design and dependencies

- **No mutable package-level state.** No global clients, registries, or config.
  Construct dependencies in `main` and pass them in (constructor injection).
- **No `init()`** except for registering truly static, deterministic data.
- **Interfaces belong to the consumer**, are small (1-3 methods), and are introduced when there is a second
  implementation or a test double, not before. Producers return concrete types.
- Verify interface compliance at compile time: `var _ llm.Provider = (*Client)(nil)`.
- Constructors: `New(...)`. Use an options struct when most callers set options; use functional options
  (`WithX(...)`) when most callers use defaults.
- Make the zero value useful where possible; document when it is not.
- Third-party dependencies need a reason in the PR description. Prefer the standard library.

## 5. Errors

- Return errors; don't panic. `panic` is for programmer bugs (impossible states) only, and
  a panic must never cross a package boundary.
- Wrap with context: `fmt.Errorf("load config %s: %w", path, err)`. Messages are lowercase,
  with no trailing punctuation and no "failed to" / "error" prefixes.
- Use `%w` when callers may need `errors.Is` or `errors.As`. Use `%v` at system boundaries to hide internals.
- Handle each error **once**: either return it or log it, never both.
- Use sentinel errors (`var ErrBudgetExceeded = errors.New(...)`) or typed errors for conditions callers branch on.
  Never match on error strings.
- `os.Exit` and `log.Fatal` are called **only in `main`**. `main` calls `run() error` and maps the result to an exit code.

## 6. Context, concurrency and lifetimes

- Every function that does I/O, calls the model, runs a tool, or may block takes `ctx context.Context` as its
  **first parameter** and honors cancellation. Never store a context in a struct.
- Every goroutine has an owner, a stop condition, and a way to wait for it (`errgroup`, `sync.WaitGroup`).
  Do not start a goroutine without a way to stop it.
- Prefer `errgroup.WithContext` for fan-out (e.g. parallel read-only tools).
- Channels are unbuffered or size 1 unless a larger size is justified in a comment. Specify direction (`<-chan`).
- Protect shared state with a mutex next to the fields it guards. Don't copy structs that contain locks.
- Tests run with `-race`, always.

## 7. Production behavior (resilience and observability)

These are the operational rules expected of production services (and the ones Netflix-style
resilience engineering is known for), applied to a CLI agent.

- **Every external call has a timeout** (model API, subprocesses, network tools), set from config.
- **Retries** only for idempotent or retry-safe calls (model API: 429/5xx/overloaded), with
  exponential backoff + jitter and a cap. Never retry tool executions automatically.
- **Hard budgets** (turns, tokens, cost, wall-clock) are enforced in the loop, not by convention.
  Every stop has an explicit, machine-readable reason.
- **Structured logging** via `log/slog`. Loggers are injected; there is no global logger in library code.
  Logs go to stderr or a file. Stdout is reserved for program output (headless JSON).
- **Run trace**: every model request/response, tool call and hook decision is a JSONL event.
  Trace events are an append-only, versioned schema.
- **Secrets never reach logs or traces**: API keys come from env/config, are redacted on output,
  and are never sent to the model.
- **Graceful shutdown**: SIGINT cancels the root context; the history stays valid (every `tool_use` has a `tool_result`).

## 8. Agent-specific invariants

These rules come from the problem domain. Code that breaks them is a bug, even if tests pass.

- **History validity**: every `tool_use` block is answered by exactly one `tool_result` with the same ID,
  in the next user message, including on errors, denials, interrupts and budget stops.
- **The loop switches on `stop_reason`**, never on "were there tool calls". Unknown stop reasons are errors.
- **Tool failures are data, not crashes**: a tool error becomes a `tool_result` with `is_error: true`.
  Go errors from tools stop the loop only for harness faults (e.g. context cancelled).
- **Tool output is bounded**: every tool truncates output to a configured limit and says it did.
- **Tool and file content is untrusted input**: it is never interpreted as instructions to the harness
  and never bypasses permission checks.
- **Permissions are checked in one place** (the pre-tool-use stage), never inside individual tools.
- **Prompt prefix stability**: the system prompt and tool list are deterministic (sorted, no timestamps in the prefix)
  so prompt caching works.
- **No provider types leak**: SDK types stay inside `internal/llm`. The rest of loom uses loom's own message types.

## 9. Testing

- Every package has tests. New behavior ships with tests in the same change.
- **Unit tests are hermetic**: no network, no API key, no real `$HOME`. Use `t.TempDir()`, `t.Setenv()`,
  and the scripted fake provider (`llmtest` package) for loop tests.
- **Integration tests** call the real Anthropic API, live behind the `integration` build tag, need
  `ANTHROPIC_API_KEY`, use the cheapest suitable model with tight budgets, and are run explicitly
  (`make test-integration`), not on every CI push.
- Table-driven tests with named fields and `t.Run` subtests. `t.Parallel()` where safe.
- Compare with `github.com/google/go-cmp/cmp` and print `diff (-want +got)`.
  Failure messages state the input, the got value and the want value: `Parse(%q) = %v, want %v`.
- `t.Fatal` for setup failures, `t.Error` for assertion failures. Never call `t.Fatal` outside the test goroutine.
- No assertion libraries. Test helpers call `t.Helper()`.
- Test doubles live in `<pkg>test` packages (e.g. `llmtest`) and are named by behavior.
- Golden files live under `testdata/` and are updated with a `-update` flag.

## 10. Documentation and comments

- Every package has a package comment (`// Package agent runs ...`).
- Every exported identifier has a doc comment that starts with its name and is a full sentence.
- Comments explain **why**, not what. Remove commented-out code.
- Document concurrency safety for mutating types, cleanup requirements (`Close`), and returned sentinel errors.
- `TODO(username): ...` must reference an issue for anything that outlives the PR.
- Design decisions that are not obvious from the code go in `docs/` (short ADR-style notes).

## 11. Configuration

- Precedence: defaults < user config (`~/.config/loom/`) < project config (`.loom/`) < env < flags.
- Config is parsed once in `main` into typed structs, validated, then passed down. Packages don't read env vars.
- Prices, model IDs and limits are config, not constants in code.

## 12. Git workflow

- `main` is always green. All work goes through a branch and a PR, even solo.
- Branches: `<type>/<short-topic>`, e.g. `feat/agent-loop`, `fix/tool-result-pairing`, `docs/standards`.
- Commits follow [Conventional Commits](https://www.conventionalcommits.org/):
  `feat(agent): stop on max_tokens with reason`. Subject in imperative mood, ≤ 72 chars; the body explains why.
- Each commit builds and passes tests (small and self-contained, per Google's "small CLs").
  Target < 400 changed lines per PR, excluding generated files and test fixtures.
- PR description: what, why, how it was tested, and anything a reviewer should look at first.
- Commits and PRs carry the author's identity only; no tool or AI attribution trailers.
- Self-review the diff before requesting review or merging. Squash-merge PRs into `main`.

## 13. Review checklist

Based on Google's code review guide, a change is ready when:

- [ ] Design: it belongs in this package and fits the architecture.
- [ ] Functionality: edge cases (cancellation, empty input, huge output, errors) are handled.
- [ ] Complexity: nothing is more general than today's need requires.
- [ ] Tests: they are correct, hermetic, and would fail if the code broke.
- [ ] Naming and comments are clear. Docs are updated.
- [ ] The section 8 invariants still hold.
- [ ] `make check` passes.
