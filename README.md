# hangar

A Go MCP server that lets orchestrators (Claude Code, Hermes, cabinet) create and manage isolated Claude Code sessions. Each "workspace" is a git worktree in a dedicated directory, opened inside a named tmux session with a running Claude Code interactive shell.

---

## Prerequisites

| Tool | Minimum version |
|------|----------------|
| Go | 1.22 |
| tmux | 3.2 |
| git | 2.35 |
| claude (Claude Code CLI) | any |

---

## Build

```sh
make build           # produces ./hangar
# or
go build -o hangar .
```

After rebuilding, reload the server in Claude Code by running `/mcp` in the Claude Code prompt. This re-registers all tools without restarting the session.

---

## Configuration

All fields are optional. Configuration can be supplied via a JSON file and/or environment variables. Environment variables always take priority.

| Field | Env var | Default | Description |
|-------|---------|---------|-------------|
| `repoPath` | `HANGAR_REPO_PATH` | auto-detected from caller's working directory | Absolute path to the git repository being managed |
| `worktreeRoot` | `HANGAR_WORKTREE_ROOT` | `<repoPath>/../worktrees` | Directory where worktrees are created |
| `storePath` | `HANGAR_STORE_PATH` | `<repoPath>/.hangar/workspaces.json` | Path to the JSON workspace registry |
| `claudeCmd` | `HANGAR_CLAUDE_CMD` | `claude` | Command to launch Claude Code |
| `idleThresholdMs` | `HANGAR_IDLE_THRESHOLD_MS` | `1000` | Milliseconds of pane inactivity before a session is "idle" |
| `sessionPrefix` | `HANGAR_SESSION_PREFIX` | `hangar-` | Prefix for tmux session names |
| `maxWorkspaces` | `HANGAR_MAX_WORKSPACES` | `100` | Hard cap on concurrent active workspaces (1–100) |

**Example config file** (`hangar-config.json`):

```json
{
  "repoPath": "/home/alice/myproject",
  "maxWorkspaces": 5,
  "idleThresholdMs": 3000
}
```

---

## Registering with Claude Code

Add the server to your Claude Code MCP config (typically `~/.claude/mcp.json` or a per-project `.mcp.json`). The JSON config file is optional — you can pass env vars only, or rely on auto-detection.

**Minimal example** (explicit repo path, no config file):

```json
{
  "mcpServers": {
    "hangar": {
      "command": "/usr/local/bin/hangar",
      "env": {
        "HANGAR_REPO_PATH": "/home/alice/myproject"
      }
    }
  }
}
```

**Zero-config example** (hangar detects the repo from its working directory):

```json
{
  "mcpServers": {
    "hangar": {
      "command": "/usr/local/bin/hangar"
    }
  }
}
```

The orchestrator Claude Code instance will then have access to all workspace tools.

---

## Registering with Hermes / cabinet

Generic stdio MCP entry (adjust `command` and `env` as needed):

```json
{
  "servers": [
    {
      "name": "hangar",
      "transport": "stdio",
      "command": "/usr/local/bin/hangar",
      "env": {
        "HANGAR_REPO_PATH": "/home/alice/myproject"
      }
    }
  ]
}
```

---

## CLI

hangar is also a standalone CLI for inspecting and managing workspaces directly.

```sh
hangar list                              # list all workspaces
hangar create <name> [--branch <branch>] # create a workspace
hangar send <id> <text>                  # send text to a session
hangar read <id> [--lines N]             # read pane output
hangar delete <id>                       # delete a workspace
```

Global flag: `--config <path>` — path to a JSON config file.

---

## Tool Reference

### `workspace_create`

Create a new workspace: git worktree + tmux session + Claude Code instance.

**Inputs:**
- `name` (string, required) — lowercase alphanumeric and hyphens, 1–40 chars
- `branch` (string, optional) — git branch to create; defaults to `name`
- `meta` (object, optional) — freeform string key-value metadata
- `prompt` (string, optional) — first prompt to send to Claude; the server waits up to 30 s for Claude to finish starting before sending, eliminating the need for a separate `workspace_send` call

**Output:** Full workspace object as JSON.

---

### `workspace_list`

List all workspaces.

**Inputs:**
- `wait_for_idle` (string, optional, default `"none"`) — `"none"` returns immediately; `"any"` blocks until at least one workspace is idle; `"all"` blocks until all workspaces are idle
- `timeout_ms` (number, optional, default 600000) — maximum wait in milliseconds when `wait_for_idle` is set

**Output:** JSON array: `[{id, name, branch, tmuxSession, worktreePath, idle}]`

---

### `workspace_send`

Send text to the Claude Code session in a workspace.

**Inputs:**
- `id` (string, required)
- `text` (string, required) — must not contain ASCII control characters (except `\n` and `\t`)
- `press_enter` (bool, optional, default true)

**Output:** `{"sent": true}`

**Guards:** Rate limited to 1 call per 200 ms per workspace.

---

### `workspace_read`

Capture recent terminal output from a workspace's tmux pane.

**Inputs:**
- `id` (string, required)
- `lines` (int, optional, default 200, max 2000)
- `wait_idle` (bool, optional, default true) — block until the pane is stable before capturing
- `timeout_ms` (int, optional, default 3600000) — maximum time to wait when `wait_idle` is true

**Output:** `{"content": "...", "captured_at": "<ISO 8601>", "idle": true/false}`

---

### `workspace_delete`

Permanently delete a workspace. **Irreversible.**

The git branch is **preserved by default** so it remains available for a PR or review. Pass `delete_branch: true` to also remove it.

**Inputs:**
- `id` (string, required)
- `confirm` (bool, required) — must be `true`
- `force` (bool, optional) — skip dirty/unpushed branch safety check
- `delete_branch` (bool, optional, default `false`) — also delete the git branch after removing the worktree

**Output:** `{"deleted": true, "id": "<id>"}`

---

## Busy/Idle Detection

The idle detector does **not** parse Claude Code's internal state. Instead:

1. Capture the last 200 lines of the tmux pane via `tmux capture-pane`.
2. SHA-256 hash the output.
3. If the hash changed since the last check → **busy** (hash + timestamp stored in the workspace registry).
4. If the hash is unchanged, compute elapsed ms since last change:
   - elapsed ≥ `idleThresholdMs` → **idle**
   - elapsed < `idleThresholdMs` → **busy**

**Tuning:** Increase `idleThresholdMs` if your Claude Code sessions take a long time to produce output between steps.

---

## Delegated Workflow

```
Orchestrator Claude Code (has hangar MCP registered)
        │
        │  workspace_create / workspace_send / workspace_read / workspace_list / workspace_delete
        ▼
  hangar binary
        │
        ├── git worktrees (one per workspace)
        └── tmux sessions (one per workspace, named hangar-<name>)
                └── Worker Claude Code instances (one per session)

Human ──► tmux attach-session -t hangar-<name>  (at any time)
```

**Typical single-worker flow:**

1. `workspace_create {name: "feat-foo", prompt: "Implement feature X"}` — workspace created, Claude Code launches, and the first prompt is sent in one call.
2. `workspace_read {id: ..., lines: 500}` — blocks until pane is stable (`wait_idle` defaults to true), then returns output.
3. Inspect output; optionally send follow-up with `workspace_send` + another `workspace_read`.
4. `workspace_delete {id: ...}` when done.

**Fan-out (parallel workers):**

1. `workspace_create` × N — spin up N workspaces.
2. `workspace_send` to each — inject prompts.
3. `workspace_list {wait_for_idle: "all"}` — block until all finish, OR call `workspace_read` per workspace in sequence (each call blocks until that pane is idle).
4. `workspace_delete` each when done.

---

## Known Limitations

- One git repository per server instance. Use separate binaries for multiple repos.
- No authentication on the stdio transport. Secure your process environment.
- Idle detection is heuristic (hash-based); a session that produces the same output repeatedly may appear idle prematurely.

---

## Troubleshooting

**"Claude Code didn't launch in tmux"**
Check `HANGAR_CLAUDE_CMD` points to a valid binary. The session is still created — attach to it manually and launch `claude` to inspect.

**"worktree already exists"**
A previous run left a stale worktree. Run `git worktree prune` in the repo, or use `workspace_delete` to clean up via the MCP interface.

**"store is out of sync"**
Delete `.hangar/workspaces.json` in the repo root and restart. Existing tmux sessions will show as untracked warnings at next startup.

**"session shows busy indefinitely"**
Increase `idleThresholdMs`. Or attach to the session manually to check whether Claude Code is actually stuck.
