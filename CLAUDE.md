# CLAUDE.md — hangar MCP Server

## Project Overview

A Go MCP server that lets orchestrators (Claude Code, Hermes, cabinet) create and manage isolated Claude Code sessions. Each workspace is a git worktree + named tmux session + running Claude Code instance.

## Go Toolchain

Minimum versions (must be met before any code phase):
- Go ≥ 1.22
- tmux ≥ 3.2
- git ≥ 2.35
- claude CLI (Claude Code)

### Key commands

```sh
go build ./...                   # compile everything
go vet ./...                     # static analysis
go test -race ./...              # unit tests with race detector
go test -race -short ./...       # skip integration tests
HANGAR_INTEGRATION=1 go test -race -tags integration ./...  # integration tests
golangci-lint run                # lint
make build                       # build binary
make test                        # unit tests (short)
make test-integration            # integration tests
make lint                        # golangci-lint
make clean                       # remove build artifacts
```

### Linter config

`.golangci.yml` must enable at minimum: `errcheck`, `govet`, `staticcheck`, `exhaustive`.

### MCP library

Use `github.com/mark3labs/mcp-go`. Do **not** use `modelcontextprotocol/go-sdk` — it has unstable APIs.

### Test libraries

`github.com/stretchr/testify` for assertions.

## Repository Layout

```
(repo root)
├── go.mod
├── go.sum
├── main.go                  entry point
├── cmd/
│   └── root.go              CLI flags
├── internal/
│   ├── config/config.go
│   ├── tmux/tmux.go
│   ├── worktree/worktree.go
│   ├── store/store.go
│   ├── workspace/workspace.go
│   ├── workspace/errors.go
│   ├── idle/idle.go
│   └── tools/tools.go
├── test/fixtures/
│   └── worktree_list_porcelain.txt
├── cmd/smoke/main.go        standalone smoke test
├── bin/                     build output (gitignored)
├── Makefile
├── .golangci.yml
└── README.md
```

All packages under `internal/` are unexported. The binary is the only artifact.

## Critical invariants

- **Never write to stdout** except via the MCP transport (`ServeStdio`). All logging goes to stderr.
- **Never use shell interpolation** in `exec.Command`. Always pass binary + args as separate strings to prevent injection.
- **Always run tests with `-race`**. The store and rate-limiter are concurrency-sensitive.
- Integration tests must carry `//go:build integration` build tag and clean up after themselves.
- The store's `flush()` must write to `<path>.tmp` then `os.Rename` atomically — never write directly.

### Process for each task

1. **Implement** — make the minimum change required by the task. No scope creep; no speculative additions.

2. **Test** — run the full unit test suite:
   ```sh
   go test -race ./...
   ```
   If integration tests are relevant to the task, also run:
   ```sh
   HANGAR_INTEGRATION=1 go test -race -tags integration ./...
   ```

3. **Fix** — if any tests fail, fix them before proceeding. Do not move on with a broken test suite.

4. **Lint** — run:
   ```sh
   golangci-lint run
   ```
   Fix all lint errors before proceeding.

5. **Check off** — mark the task complete in `tmux-claude-harness-project-plan-2.md` by changing `- [ ]` to `- [x]`.

6. **Commit** — stage the relevant files and commit using [Conventional Commits](https://www.conventionalcommits.org/). Format:
   ```
   <type>(<scope>): <short description>
   ```
   The scope is optional but encouraged — use the package name (e.g. `tmux`, `store`, `workspace`).

   | Type | When to use |
   |------|-------------|
   | `feat` | Adds, adjusts, or removes a feature of the API or UI |
   | `fix` | Fixes a bug in a preceded `feat` |
   | `refactor` | Rewrites/restructures code without changing behavior |
   | `perf` | A `refactor` that specifically improves performance |
   | `style` | White-space, formatting, missing semicolons — no behavior change |
   | `test` | Adds missing tests or corrects existing ones |
   | `docs` | Documentation only |
   | `build` | Build tools, dependencies, project version |
   | `ops` | Infrastructure, deployment, CI/CD, monitoring |
   | `chore` | Initial commit, `.gitignore`, housekeeping |

   Examples:
   ```
   feat(tmux): implement SessionExists with mock Executor
   test(store): add concurrent Add/Get stress test with -race
   build: add golangci-lint config with errcheck and staticcheck
   ```

7. **Move to the next task** — only after the commit succeeds.

### What "done" means for a task

A task is done when:
- The code compiles (`go build ./...`)
- `go vet ./...` is clean
- `go test -race ./...` passes
- `golangci-lint run` passes
- The checkbox in the plan file is marked `[x]`
- A commit exists for it

Do not batch tasks into one commit. One task = one commit.
