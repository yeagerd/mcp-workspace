# Task List: `harness-client` CLI

A human-facing command-line client that wraps every MCP tool exposed by the
tmux-harness server. The client spawns `bin/tmux-harness` as a subprocess,
connects to it over the stdio MCP transport using `mcp-go`'s client package,
and maps each subcommand to the corresponding `workspace_*` MCP tool call.

Binary: `bin/harness-client`  
Source: `client/` (repo root)  
Module path: `github.com/articulant/tmux-harness/client`

---

## Phase 1 — Scaffold

- [x] Create `client/main.go` — package `main`, calls `client.Execute()`, mirrors the
  pattern in `main.go` + `cmd/root.go`.
- [x] Create `client/client.go` — package `client`, defines `Execute()`, top-level
  `flag.FlagSet` with `--config <path>` and `--binary <path>` flags, dispatches to
  subcommand handlers. Print usage (all subcommands) when no args given; exit 1.
- [x] Add `bin/harness-client` target to `Makefile`:
  ```makefile
  build:
      go build -o bin/$(BINARY) .
      go build -o bin/harness-client ./client
  ```
  Confirm `make build` produces both binaries.

---

## Phase 2 — MCP Client Transport

- [x] Implement `client/connect.go`: function
  `connect(ctx context.Context, binary, configPath string) (*mcpclient.Client, func(), error)`
  that:
  - Resolves `binary` (default: `bin/tmux-harness` relative to the executable, then
    `$PATH`).
  - Spawns the binary with `exec.CommandContext`, passing `--config <configPath>` when
    configPath is non-empty.
  - Attaches the process's `stdin`/`stdout` as the MCP stdio transport using
    `github.com/mark3labs/mcp-go/client` (use `NewStdioMCPClient` or equivalent).
  - Returns the connected client and a `cleanup` func that kills the subprocess and
    waits for it to exit.
  - Forwards the subprocess's `stderr` to the client process's `stderr`.
- [x] Implement `client/call.go`: helper
  `callTool(ctx context.Context, c *mcpclient.Client, name string, args map[string]any) (json.RawMessage, error)`
  that calls `c.CallTool`, extracts the text content from the result, and returns the
  raw JSON bytes. Return a descriptive error when the MCP response contains an error.
- [x] Confirm `go build ./client/...` and `go vet ./client/...` are clean.

---

## Phase 3 — Subcommands

Each subcommand follows the same pattern:

1. Parse its own `flag.FlagSet` from the remaining args.
2. Call `connect(...)` to get a client + cleanup.
3. Build the args map and call `callTool(...)`.
4. Pass result to the formatter (Phase 4).
5. Return any error to `Execute()`, which prints it to stderr and exits 1.

### 3a — `list`

- [x] Implement `client/cmd_list.go`: subcommand `list`.
  - Flags: `--include-archived` (bool, default false), `--repo <alias>` (string,
    default "").
  - MCP tool: `workspace_list`.
  - Output: human-readable table (Phase 4a); honours `--json`.

### 3b — `create`

- [x] Implement `client/cmd_create.go`: subcommand `create <name>`.
  - Positional arg: `name` (required; print usage + exit 1 if missing).
  - Flags: `--branch <branch>` (string, default ""), `--repo <alias>` (string,
    default "").
  - MCP tool: `workspace_create`.
  - Output: single-workspace summary (Phase 4b); honours `--json`.

### 3c — `archive`

- [ ] Implement `client/cmd_archive.go`: subcommand `archive <id>`.
  - Positional arg: `id` (required).
  - MCP tool: `workspace_archive`.
  - Output: single-workspace summary; honours `--json`.

### 3d — `delete`

- [ ] Implement `client/cmd_delete.go`: subcommand `delete <id>`.
  - Positional arg: `id` (required).
  - Flag: `--confirm` (bool, default false). If false, print a warning and exit 1
    without calling the server.
  - MCP tool: `workspace_delete` with `confirm: true`.
  - Output: success message on stdout; honours `--json`.

### 3e — `send`

- [ ] Implement `client/cmd_send.go`: subcommand `send <id> <text>`.
  - Positional args: `id` (required), `text` (required; join remaining args as a
    single space-separated string so quoting is optional for simple prompts).
  - Flag: `--enter` (bool, default true). `--no-enter` sets it false.
  - MCP tool: `workspace_send`.
  - Output: "sent" confirmation line; honours `--json`.

### 3f — `read`

- [ ] Implement `client/cmd_read.go`: subcommand `read <id>`.
  - Positional arg: `id` (required).
  - Flag: `--lines <n>` (int, default 0 = server default).
  - MCP tool: `workspace_read`.
  - Output: print the `content` field as-is (plain text); `--json` emits the full
    JSON object.

### 3g — `idle`

- [ ] Implement `client/cmd_idle.go`: subcommand `idle <id>`.
  - Positional arg: `id` (required).
  - Flag: `--threshold-ms <n>` (int64, default 0 = server default).
  - MCP tool: `workspace_idle`.
  - Output: human-readable one-liner (`idle` / `busy`, elapsed ms); honours `--json`.
  - Exit code: 0 if idle, 2 if busy (allows `if harness-client idle <id>; then …`).

### 3h — `wait-idle`

- [ ] Implement `client/cmd_wait_idle.go`: subcommand `wait-idle <id>`.
  - Positional arg: `id` (required).
  - Flags: `--timeout-ms <n>` (int64, default 0), `--threshold-ms <n>` (int64,
    default 0), `--poll-interval-ms <n>` (int64, default 0). Zero values are omitted
    from the args map so the server uses its defaults.
  - MCP tool: `workspace_wait_idle`.
  - Output: `idle` or `timed out`; honours `--json`.
  - Exit code: 0 if idle, 2 if timed out.

### 3i — `attach-hint`

- [ ] Implement `client/cmd_attach_hint.go`: subcommand `attach-hint <id>`.
  - Positional arg: `id` (required).
  - MCP tool: `workspace_attach_hint`.
  - Output: print the shell command string on stdout (plain); honours `--json`.

---

## Phase 4 — Output Formatting

- [ ] **4a — Workspace list table** (`client/format.go`): function
  `printTable(ws []workspaceSummary, w io.Writer)` that renders a fixed-width table:
  ```
  ID        NAME              STATUS    BRANCH            REPO   CREATED
  8e9691bc  multi-repo-sup…   active    multi-repo-sup…   -      2026-06-05 13:41
  ```
  Truncate long strings with `…` to keep columns at fixed widths. Use tab-separated
  output (no external table library).
- [ ] **4b — Single workspace summary**: function `printWorkspace(ws workspaceSummary, w io.Writer)`
  that prints key-value pairs, one per line:
  ```
  id:       8e9691bc-0c72-4942-aba1-b301fef763e4
  name:     multi-repo-support
  status:   active
  branch:   multi-repo-support
  session:  harness-multi-repo-support
  worktree: /Users/yeagerd/github/articulant/worktrees/multi-repo-support
  created:  2026-06-05 13:41:55
  ```
- [ ] **4c — `--json` global flag**: add `--json` to the top-level `flag.FlagSet` in
  `client.go`. When set, all subcommands skip human-readable formatting and write
  the raw JSON returned by `callTool` to stdout (pretty-printed with
  `json.MarshalIndent`).

---

## Phase 5 — Polish

- [ ] Top-level usage: when called with no subcommand or `--help`, print a usage block
  listing every subcommand with a one-line description, then exit 0.
- [ ] Consistent error format: all errors written to stderr as
  `harness-client <subcommand>: <message>\n`; process exits with code 1.
- [ ] `--version` flag at top level: prints the same version string as the server.
- [ ] Confirm `go vet ./client/...` and `golangci-lint run ./client/...` are clean.

---

## Phase 6 — Tests

- [ ] `client/format_test.go`: unit tests for `printTable` and `printWorkspace`.
  Use `bytes.Buffer` as the writer. Cover: empty list, single entry, long-name
  truncation, archived workspace row.
- [ ] `client/connect_test.go`: test that `connect` returns an error when the binary
  path does not exist (no real subprocess needed).
- [ ] `client/call_test.go`: test `callTool` error extraction — use a mock client or
  stub that returns a pre-built MCP error response; confirm the returned `error` is
  non-nil and contains the server's message.
- [ ] Run `go test -race ./client/...` — all tests must pass.

---

## Phase 7 — Integration

- [ ] Add an integration test `client/integration_test.go` (build tag `integration`)
  that:
  - Builds `bin/tmux-harness` and `bin/harness-client` via `exec.Command("go",
    "build", ...)`.
  - Calls `harness-client list` against a temp config with an empty store.
  - Asserts exit code 0 and empty (or header-only) output.
- [ ] Run `HARNESS_INTEGRATION=1 go test -race -tags integration ./client/...` — must
  pass.
- [ ] `make build` produces both `bin/tmux-harness` and `bin/harness-client` — confirm
  with `ls bin/`.

---

## Done criteria

All tasks checked, `go build ./...`, `go vet ./...`, `go test -race ./...`, and
`golangci-lint run` pass cleanly. `make build` produces `bin/harness-client`.
Running `bin/harness-client --help` lists all subcommands.
