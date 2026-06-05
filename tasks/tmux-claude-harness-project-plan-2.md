# Project Plan: tmux + Git Worktree + Claude Code Workspace Harness (MCP Server)
### Implementation Language: Go

---

## Overview

Build a local MCP server in Go that lets Hermes, cabinet, or any MCP-compatible orchestrator create and manage isolated Claude Code sessions. Each "workspace" is a git worktree in a dedicated directory, opened inside a named tmux session with a running Claude Code interactive shell. The MCP server exposes tools to list, create, send input to, read output from, and archive these workspaces. A busy/idle detection mechanism based on terminal pane activity lets orchestrators know when a session is ready for more work.

Human operators can attach to any tmux session directly at any time — the orchestrator and human are both first-class participants.

---

## Architecture Summary

```
Orchestrator (Hermes / cabinet / etc.)
        │
        │  MCP over stdio (or HTTP/SSE stretch goal)
        ▼
  tmux-harness  (single Go binary)
        │
        ├── tmux CLI     (create/destroy/send-keys/capture-pane)
        ├── git CLI      (worktree add/remove/list/prune)
        └── claude CLI   (spawned inside tmux sessions as Claude Code)

Human operator ──► tmux attach-session -t harness-<name>
```

---

## MCP Library

Use **`github.com/mark3labs/mcp-go`**.

- Battle-tested: 8k+ GitHub stars, imported by 400+ packages
- Implements MCP spec 2025-11-25 with backward compatibility
- Clean, idiomatic Go API for tool registration, stdio/SSE transport, and resource exposure
- If migration to the official `modelcontextprotocol/go-sdk` becomes desirable later, a clear upgrade path exists

Do **not** use `modelcontextprotocol/go-sdk` for this project — it is still stabilizing and has a history of breaking API changes.

---

## Repository Layout (target)

```
(repo root)
├── go.mod
├── go.sum
├── main.go                  entry point: config loading, server startup
├── cmd/
│   └── root.go              CLI flag parsing (config path, version flag)
├── internal/
│   ├── config/
│   │   └── config.go        config struct, load/validate logic
│   ├── tmux/
│   │   └── tmux.go          thin wrapper around tmux CLI
│   ├── worktree/
│   │   └── worktree.go      thin wrapper around git worktree CLI
│   ├── store/
│   │   └── store.go         JSON-file workspace registry
│   ├── workspace/
│   │   └── workspace.go     lifecycle coordinator (create/archive/delete)
│   ├── idle/
│   │   └── idle.go          busy/idle detection
│   └── tools/
│       └── tools.go         MCP tool handlers (thin layer over workspace pkg)
├── bin/                     build output (gitignored)
├── test/
│   └── fixtures/
│       └── worktree_list_porcelain.txt   test fixture
└── README.md
```

All packages under `internal/` are unexported to external consumers — the binary is the only artifact.

---

## Phase 0 — Prerequisites & Environment Check

**Goal:** Confirm the development machine is ready and establish project scaffolding before writing any substantive code.

### Checklist

- [x] Verify Go ≥ 1.22 is installed (`go version`). Document minimum version in README and `go.mod`.
- [x] Verify `tmux` ≥ 3.2 is installed (`tmux -V`). Document in README.
- [x] Verify `git` ≥ 2.35 is installed. Document in README.
- [x] Verify the `claude` CLI (Claude Code) is installed and `claude --version` succeeds.
- [x] Run `go mod init github.com/<your-org>/tmux-harness` (or an appropriate module path).
- [x] Add `github.com/mark3labs/mcp-go` as a dependency (`go get github.com/mark3labs/mcp-go`).
- [x] Add `github.com/stretchr/testify` for test assertions (`go get github.com/stretchr/testify`).
- [x] Set up a `Makefile` with targets: `build`, `test`, `test-integration`, `lint`, `clean`.
- [x] Install `golangci-lint` and add a `.golangci.yml` config with at minimum: `errcheck`, `govet`, `staticcheck`, `exhaustive`.
- [x] Write a trivial `main.go` that creates an `mcp-go` server, registers one no-op tool (`ping`), and serves over stdio. Confirm a client (MCP Inspector or `mcp-client` CLI) can connect and list tools.
- [x] Add a `.gitignore` (the `tmux-harness` binary, any `*.worktree` dirs, `config.json` if local).

---

## Phase 1 — Configuration

**Goal:** Centralize all runtime configuration in one typed struct, loaded at startup.

### Checklist

- [x] Define a `Config` struct in `internal/config/config.go` with the following fields:
  - `RepoPath string` — absolute path to the git repo being managed
  - `WorktreeRoot string` — directory where worktrees are created (default: `<RepoPath>/../worktrees`)
  - `StorePath string` — path to the JSON workspace registry (default: `~/.config/tmux-harness/workspaces.json`)
  - `ClaudeCmd string` — command to launch Claude Code (default: `claude`)
  - `IdleThresholdMs int` — milliseconds of terminal inactivity before a session is "idle" (default: `5000`)
  - `SessionPrefix string` — prefix for tmux session names (default: `harness-`)
  - `MaxWorkspaces int` — hard cap on active workspaces (default: `10`)
- [x] Implement `Load(configPath string) (*Config, error)` that reads a JSON file at `configPath` (if present) and then overrides each field with environment variables (`HARNESS_REPO_PATH`, `HARNESS_WORKTREE_ROOT`, etc.). Env vars take priority over file values.
- [x] Implement `Validate(cfg *Config) error` that checks:
  - `RepoPath` exists on disk
  - `RepoPath` contains a `.git` directory or file
  - `WorktreeRoot` parent exists (create the directory itself if absent)
  - `StorePath` parent directory exists (create if absent)
  - `MaxWorkspaces` is between 1 and 50
- [x] On startup, print a summary of resolved config values to `stderr` (never `stdout` — stdout is the MCP transport stream). Mark any values that are defaults.
- [x] Write unit tests for `Load` and `Validate` covering: missing file (uses defaults), env var overrides, invalid `RepoPath`, missing `.git`.

---

## Phase 2 — tmux Abstraction Layer

**Goal:** A package that wraps all tmux CLI interactions. No other package in the project shells out to tmux directly.

### Key concepts

- A **session** in this project is a named tmux session (`harness-<workspace-name>`).
- All reads use `tmux capture-pane -p -t <session>` to get the visible pane buffer.
- All writes use `tmux send-keys -t <session> <text> Enter`.

### Checklist

- [x] Write `internal/tmux/tmux.go`. All functions must shell out using `exec.Command` (never `exec.CommandContext` with a shell string — always pass the binary and args separately to avoid injection).
- [x] Implement the following functions. All are `func(...) error` or `func(...) (T, error)` — never panic on failure:
  - `SessionExists(prefix, name string) (bool, error)` — runs `tmux list-sessions -F #{session_name}` and checks for `prefix+name`
  - `NewSession(sessionName, startDir string) error` — creates a detached session (`-d`) in `startDir`
  - `KillSession(sessionName string) error` — kills a session; if the session does not exist, return `nil` (not an error)
  - `SendKeys(sessionName, text string, pressEnter bool) error` — sends text to the pane; appends `Enter` if `pressEnter` is true
  - `CapturePane(sessionName string, lines int) (string, error)` — captures the last `lines` lines of output; use `-S -<lines>` flag
  - `ListSessions(prefix string) ([]string, error)` — returns all session names that start with `prefix`
  - `RenameSession(oldName, newName string) error`
- [x] Define a sentinel `ErrSessionNotFound` error so callers can distinguish "session missing" from other failures.
- [x] Handle the case where the tmux server process is not running — the first `NewSession` call starts it implicitly; subsequent calls to `ListSessions` on an empty server should return an empty slice, not an error.
- [x] Write unit tests for all functions. Use an interface `Executor` (with a single `Run(name string, args ...string) ([]byte, error)` method) injected into the tmux client struct so tests can mock shell calls without actually invoking tmux. Test the happy path and at least one error path per function.
- [x] Write a `porcelain` parser for `list-sessions` output as a separate internal function so it can be tested with a fixture string independently of any exec call.

---

## Phase 3 — Git Worktree Abstraction Layer

**Goal:** A package wrapping all git worktree operations. No other package calls git directly.

### Key git commands

- `git worktree add <path> [-b <branch>]` — creates a worktree
- `git worktree list --porcelain` — lists all worktrees with metadata
- `git worktree remove <path> [--force]` — removes a worktree
- `git worktree prune` — cleans up stale metadata

### Checklist

- [x] Write `internal/worktree/worktree.go`. Accept the repo path as a struct field so all `git` invocations use `-C <repoPath>` to set the working directory rather than relying on the process's cwd.
- [x] Define a `WorktreeInfo` struct:
  ```go
  type WorktreeInfo struct {
      Path    string
      Branch  string  // empty string if detached HEAD
      Head    string  // commit SHA
      Locked  bool
      Prunable bool
  }
  ```
- [x] Implement:
  - `Add(worktreePath, branchName string, createBranch bool) error` — if `createBranch` is true, pass `-b branchName`; otherwise check out existing branch
  - `Remove(worktreePath string, force bool) error`
  - `List() ([]WorktreeInfo, error)` — parse `--porcelain` output into `[]WorktreeInfo`
  - `Prune() error`
- [x] The porcelain parser must handle all four known field types: `worktree`, `HEAD`, `branch`, `locked`, `prunable`, and the bare/detached cases. Write it as a pure function (takes a string, returns `[]WorktreeInfo`) with no exec dependency.
- [x] Store the porcelain parser test fixture in `test/fixtures/worktree_list_porcelain.txt` covering: main worktree, a regular worktree on a branch, a detached worktree, a locked worktree, and a prunable worktree.
- [x] Handle the edge case in `Add` where `worktreePath` already exists on disk but is not registered with git — surface a typed error `ErrWorktreePathExists` so the caller can decide whether to force.
- [x] Write unit tests for all functions using the same `Executor` interface pattern as the tmux package.

---

## Phase 4 — Workspace Registry (Persistent State)

**Goal:** A durable, thread-safe JSON registry of all workspaces, persisted to disk.

### Workspace type

Define in `internal/store/store.go`:

```go
type WorkspaceStatus string

const (
    StatusActive   WorkspaceStatus = "active"
    StatusArchived WorkspaceStatus = "archived"
    StatusOrphaned WorkspaceStatus = "orphaned"  // tmux session missing at startup
)

type Workspace struct {
    ID              string            `json:"id"`            // UUID v4
    Name            string            `json:"name"`          // slug; also used in session name
    TmuxSession     string            `json:"tmuxSession"`   // full session name: "<prefix><name>"
    WorktreePath    string            `json:"worktreePath"`
    Branch          string            `json:"branch"`
    Status          WorkspaceStatus   `json:"status"`
    CreatedAt       time.Time         `json:"createdAt"`
    ArchivedAt      *time.Time        `json:"archivedAt,omitempty"`
    LastCaptureHash string            `json:"lastCaptureHash"`
    LastChangedAt   time.Time         `json:"lastChangedAt"`
    Meta            map[string]string `json:"meta,omitempty"`
}
```

### Checklist

- [x] The `Store` struct must hold a `sync.RWMutex` and protect all reads and writes. The store will be accessed from multiple goroutines (background idle polling + MCP request handlers).
- [x] Implement `NewStore(path string) (*Store, error)` — creates the file and parent dirs if absent; loads existing data if the file exists.
- [x] Implement the following methods on `*Store`. Each write method must call an internal `flush()` that atomically writes to a temp file then renames it over the target (avoids corruption on crash):
  - `Add(ws Workspace) error` — rejects if `ws.Name` already exists with status `active`
  - `Get(id string) (Workspace, error)`
  - `GetByName(name string) (Workspace, error)`
  - `List(includeArchived bool) []Workspace`
  - `Update(id string, apply func(*Workspace)) error` — applies a mutation function and flushes
  - `Delete(id string) error` — hard-deletes from the JSON file (only used for cleanup; normal flow uses `Update` to set status)
- [x] Use `crypto/rand` (or `github.com/google/uuid` if you prefer a library) to generate workspace IDs.
- [x] Write unit tests using `t.TempDir()` for the file path. Test concurrent access using `t.Parallel()` and at least 10 goroutines calling `Add`/`Get`/`List` simultaneously to surface race conditions. Always run tests with `-race`.

---

## Phase 5 — Workspace Lifecycle Coordinator

**Goal:** Combine the tmux, worktree, and store packages into a single high-level coordinator that is the only package the MCP tool handlers call directly.

### Checklist

- [x] Write `internal/workspace/workspace.go`. Define a `Manager` struct that holds references to the tmux client, worktree client, store, and config.
- [x] Implement `Create(ctx context.Context, opts CreateOptions) (store.Workspace, error)`:
  - `CreateOptions` fields: `Name string`, `Branch string` (optional), `Meta map[string]string`
  - Validate `Name`: lowercase alphanumeric and hyphens only, 1–40 chars, no reserved words (`new`, `list`, `delete`). Return a typed `ErrInvalidName` if invalid.
  - Reject if `store.List(false)` already contains an active workspace with that name.
  - Enforce `MaxWorkspaces` — return `ErrCapacityReached` if at limit.
  - Call `worktree.Add(...)`. On failure, return immediately (nothing to clean up yet).
  - Call `tmux.NewSession(sessionName, worktreePath)`. On failure, call `worktree.Remove(...)` before returning.
  - Wait 300 ms for tmux to settle (use `time.Sleep` — this is acceptable here; document why).
  - Call `tmux.SendKeys(sessionName, cfg.ClaudeCmd, true)` to launch Claude Code.
  - Call `store.Add(ws)`. On failure, kill the tmux session and remove the worktree before returning.
  - Return the workspace.

- [x] Implement `Archive(ctx context.Context, id string) (store.Workspace, error)`:
  - Look up workspace; return `ErrNotFound` if missing, `ErrAlreadyArchived` if status is not `active`.
  - Send `exit` to the tmux session via `SendKeys`.
  - Poll `tmux.SessionExists` every 200 ms for up to 5 s. If still alive after 5 s, call `tmux.KillSession`.
  - Call `worktree.Remove(worktreePath, false)`. If it fails with a "dirty worktree" error from git, try `Remove(..., true)` (force) and log a warning to stderr.
  - Call `store.Update` to set `status = archived` and `archivedAt`.

- [x] Implement `Delete(ctx context.Context, id string) error`:
  - Like `Archive` but also calls `git branch -d <branch>` (or `-D` if force). This is the only destructive-to-git operation; document it prominently.
  - Callers must pass `confirmed bool` — if false, return `ErrDeleteNotConfirmed` without doing anything.

- [x] Implement `List(includeArchived bool) []store.Workspace` — direct pass-through to store.

- [x] Implement `Get(id string) (store.Workspace, error)` and `GetByName(name string) (store.Workspace, error)`.

- [x] Implement `Reconcile(ctx context.Context) error` — called at startup:
  - For each workspace with status `active`, call `tmux.SessionExists`.
  - If the session no longer exists, call `store.Update` to set status `orphaned` and log the workspace name to stderr.
  - If a tmux session with the harness prefix exists but is not in the store, log a warning (do not auto-delete — the operator may have created it manually).

- [x] Define all error types as exported sentinel values in a `workspace/errors.go` file (e.g. `var ErrNotFound = errors.New("workspace not found")`). Use `fmt.Errorf("...: %w", ErrNotFound)` for wrapping.

- [x] Write integration tests tagged with `//go:build integration` build tag. These tests actually invoke tmux and git. They must clean up after themselves (defer archive/delete) and be skippable when tmux is not available (`testing.Short()` or an env var `HARNESS_INTEGRATION=1`).

---

## Phase 6 — Busy/Idle Detection

**Goal:** Determine whether a Claude Code session is currently working or waiting for input, using pane output hashing.

### Design

Do not attempt to parse Claude Code's internal state. Instead: capture the tmux pane, hash its content, and compare to the previous hash. If the hash changed since the last check, the session is busy. If the hash has been unchanged for longer than the configured threshold, it is idle.

### Checklist

- [x] Write `internal/idle/idle.go`.
- [x] Define `IdleStatus`:
  ```go
  type IdleStatus struct {
      Idle          bool
      LastChangedAt time.Time
      ElapsedMs     int64
      ThresholdMs   int64
  }
  ```
- [x] Implement `Check(ctx context.Context, ws store.Workspace, tmuxClient *tmux.Client, store *store.Store, thresholdMs int64) (IdleStatus, error)`:
  1. Call `tmuxClient.CapturePane(ws.TmuxSession, 200)`.
  2. Hash the result with `crypto/sha256` (convert to hex string).
  3. If hash differs from `ws.LastCaptureHash`, call `store.Update` to record the new hash and set `LastChangedAt = time.Now()`. Return `IdleStatus{Idle: false, ...}`.
  4. If hash is the same, compute `elapsedMs = time.Since(ws.LastChangedAt).Milliseconds()`. If `elapsedMs >= thresholdMs`, return `IdleStatus{Idle: true, ...}`. Otherwise return `IdleStatus{Idle: false, ...}`.
- [x] Optionally (behind a config flag `EnablePromptHeuristic bool`): after determining the hash state, also check whether the last non-empty line of the captured pane ends with the Claude Code prompt string (a configurable string, default `"> "`). If it does, treat this as additional evidence of idleness but not a definitive signal — only use it as a tiebreaker when `elapsedMs` is between 80% and 100% of `thresholdMs`.
- [x] Write unit tests for `Check` by passing a mock tmux client. Test: first call (hash is empty → always busy), hash changes → busy, hash stable below threshold → busy, hash stable above threshold → idle.

---

## Phase 7 — MCP Server & Tool Registration

**Goal:** Expose the workspace manager as a proper MCP server using `mcp-go`.

### Transport

Default transport: **stdio**. This is how Claude Code's `--mcp-config` spawns MCP servers — as child processes communicating over stdin/stdout.

Stretch goal: HTTP/SSE transport for multi-client use (see Phase 11).

### Tool Definitions

Register all tools in `internal/tools/tools.go`. Each handler must:
- Extract and validate arguments from `mcp.CallToolRequest` using `mcp-go`'s typed helper methods (`RequireString`, `RequireBool`, etc.)
- Return `mcp.NewToolResultError(...)` for user-facing errors (not Go `error` returns), so the orchestrator receives a structured error rather than a transport-level failure
- Log unexpected internal errors to `stderr`

---

#### `workspace_list`
- **Description:** List all workspaces. By default excludes archived ones.
- **Inputs:** `include_archived` (bool, optional, default false)
- **Output:** JSON array of workspace summaries (id, name, status, branch, tmuxSession, createdAt, worktreePath).

---

#### `workspace_create`
- **Description:** Create a new workspace: git worktree + tmux session + Claude Code instance.
- **Inputs:**
  - `name` (string, required) — slug for the workspace
  - `branch` (string, optional) — git branch to create or check out; defaults to `name`
  - `meta` (object, optional) — freeform string key-value metadata
- **Output:** Full workspace object as JSON.
- **Side effects:** Creates worktree on disk, starts tmux session, launches Claude Code.

---

#### `workspace_archive`
- **Description:** Gracefully shut down a workspace. Quits Claude Code, removes the worktree, retains the git branch.
- **Inputs:** `id` (string, required)
- **Output:** Updated workspace object with `status: "archived"`.

---

#### `workspace_delete`
- **Description:** Permanently delete a workspace and its git branch. Destructive and irreversible.
- **Inputs:**
  - `id` (string, required)
  - `confirm` (bool, required) — must be `true`; tool returns an error if `false` or absent
- **Output:** `{"deleted": true, "id": "<id>"}`.

---

#### `workspace_send`
- **Description:** Send text (a prompt or command) to the Claude Code session in a workspace.
- **Inputs:**
  - `id` (string, required)
  - `text` (string, required)
  - `press_enter` (bool, optional, default true)
- **Output:** `{"sent": true}`.
- **Guards:** Reject if workspace is not `active`. Sanitize `text`: strip ASCII control characters `\x00`–`\x1f` except `\n` and `\t`; return a tool error if any were present (do not silently strip — the orchestrator should know it sent bad input).
- **Rate limit:** No more than one send per workspace per 200 ms. Track last-send time in memory (not the store). Return a tool error with a `retry_after_ms` field if exceeded.

---

#### `workspace_read`
- **Description:** Capture recent terminal output from a workspace's tmux pane.
- **Inputs:**
  - `id` (string, required)
  - `lines` (int, optional, default 200, max 2000)
- **Output:** `{"content": "<pane text>", "captured_at": "<ISO 8601>"}`.

---

#### `workspace_idle`
- **Description:** Check whether a workspace is busy or idle based on pane output change detection.
- **Inputs:**
  - `id` (string, required)
  - `threshold_ms` (int, optional) — override the configured default
- **Output:** IdleStatus object: `{"idle": bool, "last_changed_at": "...", "elapsed_ms": N, "threshold_ms": N}`.

---

#### `workspace_attach_hint`
- **Description:** Return the shell command a human should run to attach to this workspace's tmux session.
- **Inputs:** `id` (string, required)
- **Output:** `{"command": "tmux attach-session -t harness-<name>"}`.

---

### MCP Resource

- [x] Register an MCP resource with URI template `workspace://{id}/pane` that returns the current pane content as a text resource. Orchestrators can fetch this directly without using `workspace_read`.
- [ ] Resource list (`resources/list`) must enumerate all active workspaces with their pane URI.

### Additional Checklist

- [x] In `main.go`, construct the MCP server, call `s.AddTool(...)` for each tool, and call `server.ServeStdio(s)` to block.
- [x] Wrap the server in a `context.Context` that is cancelled on `SIGINT`/`SIGTERM`, giving in-flight operations up to 5 s to finish before hard exit.
- [x] Write a standalone smoke-test script in `cmd/smoke/main.go` that connects to the server over stdio, calls every tool in sequence, and exits non-zero on any failure. This is not part of `go test` — run it manually or in CI as a separate step.

---

## Phase 8 — Startup Sequence

**Goal:** Define the precise order of operations when the binary starts, to ensure safe and predictable behavior.

### Checklist

The `main()` function must execute the following in order:

- [x] Parse CLI flags: `--config <path>`, `--version`.
- [x] Load and validate config (Phase 1).
- [x] Initialize the store (Phase 4). If the store file is corrupt (invalid JSON), log the error and exit — do not silently overwrite.
- [x] Initialize the tmux and worktree clients.
- [x] Initialize the workspace manager.
- [x] Call `manager.Reconcile()` to detect orphaned workspaces. Log results to stderr.
- [x] Build and start the MCP server (Phase 7).

All steps before MCP server startup must write only to `stderr`. The MCP server owns `stdout` from the moment `ServeStdio` is called.

---

## Phase 9 — Error Handling, Resilience & Safety

### Checklist

- [x] **Partial failure cleanup in `Create`:** If any step fails after a previous step succeeded, the cleanup path must undo all prior steps before returning. Test this with an integration test that injects a failure at each stage.
- [x] **Atomic store writes:** The store's `flush()` must write to `<storePath>.tmp` and then use `os.Rename` to replace the target. Verify on Linux that `os.Rename` is atomic when source and destination are on the same filesystem (they will be, since `.tmp` is in the same directory).
- [x] **Max workspace enforcement:** `Create` must check the count of active workspaces against `MaxWorkspaces` before touching the filesystem. Return `ErrCapacityReached`.
- [x] **Input sanitization for `workspace_send`:** Defined in Phase 7. Do not silently strip — return an error.
- [x] **Rate limiting for `workspace_send`:** Use a `map[string]time.Time` (keyed by workspace ID) protected by a `sync.Mutex`. No external dependency needed.
- [x] **Destructive operation guards:** `Archive` and `Delete` must look up the workspace in the store before touching tmux or git. If the workspace is not in the store, return `ErrNotFound` — never infer state from the filesystem alone.
- [x] **Graceful shutdown:** On `SIGINT`/`SIGTERM`, the server must stop accepting new tool calls, wait for in-flight calls to complete (with a 5 s timeout), and exit cleanly. Active tmux sessions and worktrees are left intact — they are not cleaned up on server exit.
- [x] **No shell injection:** Every `exec.Command` call in the tmux and worktree packages must pass arguments as separate `string` values, never via shell interpolation. Add a linter rule or code comment in each package reminding future contributors of this.

---

## Phase 10 — Claude Code Integration & Wiring

**Goal:** Document and validate the end-to-end connection between Claude Code (as orchestrator), the harness MCP server, and the worker Claude Code sessions.

### Checklist

- [x] Write an `mcp-config.example.json` file showing how to register the harness server in Claude Code's MCP config. The server entry must use the `command` transport type, pointing to the compiled binary.
- [x] Document how to register the server in Hermes/cabinet's MCP config (generic stdio entry).
- [ ] Verify that all seven tools and the pane resource are visible in Claude Code after registering the server (use the MCP Inspector tool or `/mcp` slash command in Claude Code).
- [x] Write a README section titled "Two-Claude Setup" that explains the topology:
  - One Claude Code instance = the orchestrator (Hermes). It has the harness MCP registered.
  - N Claude Code instances = workers. They run inside the harness's tmux sessions.
  - The orchestrator creates workspaces, sends prompts, polls for idleness, reads output, and archives when done.
  - The human operator can attach to any worker session at any time without disrupting the orchestrator.
- [ ] Manually test the full round-trip:
  1. Orchestrator calls `workspace_create`.
  2. Orchestrator calls `workspace_send` with a prompt.
  3. Orchestrator polls `workspace_idle` until `idle: true`.
  4. Orchestrator calls `workspace_read` to retrieve output.
  5. Human attaches to the session with `tmux attach-session` mid-flow and types something.
  6. Orchestrator calls `workspace_archive`.

---

## Phase 11 — README & Documentation

### Checklist

- [x] README must include, in this order:
  - [x] One-paragraph description of what the project does
  - [x] Prerequisites (tmux version, git version, Go version, Claude Code CLI)
  - [x] Build instructions (`make build` or `go build -o tmux-harness .`)
  - [x] Configuration reference (all fields, env var names, defaults)
  - [x] How to register with Claude Code (`--mcp-config` example)
  - [x] How to register with Hermes / cabinet (generic stdio MCP config)
  - [x] All tool descriptions with input/output schemas
  - [x] Busy/idle detection: how it works and how to tune `threshold_ms`
  - [x] How to manually attach to a session
  - [x] Startup reconciliation behavior (what happens to orphaned workspaces)
  - [x] Known limitations (single-repo per server instance, no auth on stdio)
  - [x] Troubleshooting: "Claude Code didn't launch in tmux", "worktree already exists", "store is out of sync", "session shows busy indefinitely"

---

## Phase 12 — Stretch Goals

Do these only after the core is solid and tested.

- [ ] **HTTP/SSE transport:** Run the server as an HTTP server using `mcp-go`'s SSE transport, so multiple orchestrators can share one harness. Add bearer-token authentication (single shared token in config).
- [ ] **`workspace_recover` tool:** Expose the reconcile logic as an MCP tool so an orchestrator can trigger recovery without restarting the server.
- [ ] **Multi-repo support:** Allow `workspace_create` to accept an optional `repo_path` parameter to manage worktrees in different repos.
- [ ] **Workspace templates:** Accept a `setup_script` path in `workspace_create` that is executed inside the new worktree before Claude Code launches (e.g. `npm install`, copy `.env`).
- [ ] **`workspace_watch` resource subscriptions:** Push pane diffs as MCP resource update notifications so the orchestrator can react without polling `workspace_idle`.
- [ ] **Web UI:** A local HTML dashboard served by the HTTP transport mode showing workspace status, live pane content (via SSE), and archive/delete buttons.

---

## Testing Strategy

| Package | Test type | How |
|---|---|---|
| `internal/config` | Unit | `testing` + `testify`; temp dirs |
| `internal/tmux` | Unit | Mock `Executor` interface; fixture strings for output parsing |
| `internal/worktree` | Unit | Mock `Executor` interface; `test/fixtures/worktree_list_porcelain.txt` |
| `internal/store` | Unit | `t.TempDir()`; `-race` flag for concurrency |
| `internal/idle` | Unit | Mock tmux client |
| `internal/workspace` | Integration (`//go:build integration`) | Requires real tmux + git; cleans up after itself |
| `internal/tools` | Unit | Mock `Manager` interface |
| Full flow | Manual / E2E | Smoke-test script + human verification |

**Always run `go test -race ./...` in CI.** The store and rate-limiter are the most likely sources of data races.

Run unit tests: `make test` → `go test ./... -short`
Run integration tests: `make test-integration` → `HARNESS_INTEGRATION=1 go test ./... -tags integration`

---

## Definition of Done (v1)

The project is complete when:

1. `go build ./...` succeeds with zero errors and `go vet ./...` is clean.
2. `golangci-lint run` passes with the configured ruleset.
3. All unit tests pass under `go test -race ./...`.
4. The smoke-test script completes end-to-end successfully.
5. An orchestrator (Claude Code with `--mcp-config`, Hermes, or cabinet) can: create a workspace, send a prompt, poll until idle, read output, and archive — with zero manual intervention.
6. A human operator can `tmux attach-session -t harness-<name>` and interact with the worker Claude Code session while the orchestrator is simultaneously using `workspace_send` and `workspace_read`.
7. The binary is a single static file with no runtime dependencies. `ldd tmux-harness` should report "not a dynamic executable" on Linux (use `CGO_ENABLED=0` if needed).
8. README is complete and a new developer can go from zero to a working server in under 15 minutes.
