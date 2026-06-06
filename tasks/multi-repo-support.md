# Multi-Repo Support

## Problem

The harness binary is currently locked to a single repository. `config.Config` holds one
`RepoPath`/`WorktreeRoot` pair, validated at startup in `config.Validate()`. A single
`worktree.Client` is constructed from that path in `cmd/root.go:57` and passed to the
`workspace.Manager`. There is no way to create a workspace in a different repo without
starting a second binary instance.

MCP clients (Claude Code, Hermes) are typically already running inside their own git
repos and want a shared long-running harness they can call to manage worktrees across
multiple projects.

---

## Current implementation — where the single-repo assumption lives

| File | Location | What it does |
|------|----------|--------------|
| `internal/config/config.go:17` | `Config.RepoPath` | Single repo path field |
| `internal/config/config.go:81–94` | `Validate()` | Fails if `RepoPath` is empty or has no `.git` |
| `internal/config/config.go:52–54` | `Load()` | Derives `WorktreeRoot` as `../worktrees` relative to `RepoPath` |
| `cmd/root.go:57` | `worktree.New(cfg.RepoPath)` | Creates one `worktree.Client` for the whole process |
| `internal/workspace/workspace.go:33–36` | `Manager` struct | Holds a single `*worktree.Client` |
| `internal/workspace/workspace.go:70` | `Create()` | Worktree path is `filepath.Join(m.cfg.WorktreeRoot, opts.Name)` |
| `internal/workspace/workspace.go:192–196` | `Delete()` | Calls `git -C m.cfg.RepoPath branch -d` directly |
| `internal/store/store.go:27–38` | `Workspace` struct | No `RepoPath` field; workspaces are not repo-tagged |
| `internal/tools/tools.go:151–185` | `workspace_create` tool | No `repo` parameter |

---

## Approaches

### Approach A — Static config with a named-repos map (recommended)

Config file declares a map of alias → repo entry. Each entry has `path` and optional
`worktreeRoot`. `workspace_create` accepts an optional `repo` parameter (alias or
absolute path). The Manager holds a `map[string]*worktree.Client`, keyed by alias.

**Trade-offs:**
- Pro: repos are declared up-front; typos in paths caught at startup
- Pro: aliases are stable identifiers callers can hardcode
- Pro: no on-the-fly filesystem access at call time
- Con: adding a new repo requires a config change and binary restart
- Con: slightly more complex config schema

### Approach B — Per-call `repo_path` parameter (dynamic)

`workspace_create` gets an optional `repo_path` string. The Manager validates and
creates a `worktree.Client` on first use per path, caching them in a sync.Map.
No repo declaration needed in config.

**Trade-offs:**
- Pro: zero config friction; callers just pass the path
- Pro: no restart required when working with a new repo
- Con: repo paths are validated lazily, so errors surface at call time not startup
- Con: `worktreeRoot` must be derived or also passed per call (more caller burden)
- Con: harder to audit which repos the harness is touching

### Approach C — Multiple binary instances sharing a store

Run one `tmux-harness` binary per repo (each with its own `--config`). Route calls
by running the right instance. The shared state concern is handled by giving each
instance its own `StorePath` namespace (e.g. `~/.config/tmux-harness/<alias>.json`).

**Trade-offs:**
- Pro: zero code changes to core logic; each instance is already correct
- Pro: full isolation — a crash in one repo doesn't affect others
- Con: client must know which instance to talk to (separate stdio process per repo)
- Con: MCP protocol is 1:1 stdio; orchestrator must manage N connections
- Con: does not satisfy the "single binary, single MCP endpoint" goal

### Approach D — Environment variable list (lightweight variant of A)

`HARNESS_REPOS` is a colon-separated list of `alias=path` pairs. No config file changes.
Good enough for 2–3 repos; becomes unwieldy past that.

**Trade-offs:**
- Pro: works today without any config file schema changes
- Pro: easy to set in MCP client JSON
- Con: no per-repo `worktreeRoot` override; all must follow the default convention
- Con: not composable with a config file cleanly

---

## Decision

**Approach A** (static config, named-repos map) is recommended. It validates paths at
startup, keeps the config file as the single source of truth, and maps cleanly onto the
existing `Config` → `Validate` → `Manager` wiring. Approach B can be layered on later
as a "register a repo on the fly" escape hatch if needed.

---

## Task List

### Phase 1 — Config schema: named repos

- [x] Add `Repo` struct to `internal/config/config.go`:
  ```go
  type Repo struct {
      Path        string `json:"path"`
      WorktreeRoot string `json:"worktreeRoot"` // optional; defaults to ../worktrees
  }
  ```
- [x] Add `Repos map[string]Repo` field to `Config` (alongside the existing `RepoPath` for
  backward compat in this phase).
- [x] In `config.Load()`, after the JSON file is parsed, derive each repo's `WorktreeRoot`
  if empty (same `filepath.Dir(path)+"/worktrees"` logic used today for `WorktreeRoot`).
- [x] In `config.Validate()`, if `Repos` is non-empty, iterate and validate each entry:
  path exists, has `.git`, `WorktreeRoot` parent exists, create `WorktreeRoot` dir.
  If `Repos` is empty and `RepoPath` is set, synthesise a `Repos` entry with the alias
  `"default"` so downstream code sees one canonical shape (backward compat shim).
- [x] If both `Repos` and `RepoPath` are set, return an error (ambiguous config).
- [x] Add `HARNESS_REPOS` env var support: `alias=path[:worktreeRoot],…` comma-separated,
  parsed in `config.Load()` after file load, same priority as other env vars.
- [x] Update `config.PrintSummary()` to iterate `cfg.Repos` and print each alias/path pair.
- [x] Update `internal/config/config_test.go`:
  - Test `Load()` with a config file containing `repos`.
  - Test `Validate()` accepts a valid multi-repo config.
  - Test `Validate()` rejects config with both `repos` and `repoPath` set.
  - Test backward compat: config with only `repoPath` synthesises a `"default"` entry.

### Phase 2 — Manager: per-repo worktree clients

- [x] Replace `Manager.worktree *worktree.Client` with
  `Manager.worktrees map[string]*worktree.Client` (keyed by alias).
- [x] Update `workspace.New()` to accept `map[string]*worktree.Client` instead of a single
  client.
- [x] Update `cmd/root.go`: replace `worktree.New(cfg.RepoPath)` with a loop over `cfg.Repos`
  building the map, then pass the map to `workspace.New()`.
- [x] Add a `repoAlias` helper on `Manager` that looks up the client for an alias, returning
  `ErrUnknownRepo` if not found (add `ErrUnknownRepo` to `internal/workspace/errors.go`).

### Phase 3 — Store: tag workspaces with their repo

- [x] Add `RepoAlias string` and `RepoPath string` fields to `store.Workspace`
  (`internal/store/store.go:27`).
- [x] Update `workspace.Create()` to populate both fields on the `store.Workspace` it writes.
- [x] Update `workspaceSummary` in `internal/tools/tools.go:69` to include `repoAlias` and
  `repoPath` in JSON output.
- [x] Name-conflict check in `workspace.Create()` (currently in `workspace.go:53`) must be
  scoped to the same `RepoAlias` so two repos can have a workspace named `"feat-x"`.
- [x] `store.Add()` — update the duplicate-name guard to also compare `RepoAlias`.

### Phase 4 — Tool API: `repo` parameter on `workspace_create`

- [x] Add optional `repo` string parameter to `workspace_create` MCP tool
  (`internal/tools/tools.go:151`). Description: "Alias of the repo to create the
  workspace in (defaults to \"default\" if only one repo is configured)".
- [x] In the tool handler, resolve the alias: if `repo` is empty and there is exactly one
  repo configured, use it; if empty and multiple repos are configured, return an error
  asking the caller to specify.
- [x] Add `Repo string` field to `workspace.CreateOptions`.
- [x] In `workspace.Manager.Create()`, use `opts.Repo` to select the right `worktree.Client`
  and the right `WorktreeRoot` for the worktree path.
- [x] Update `workspace.Manager.Delete()` — currently calls `git -C m.cfg.RepoPath`
  directly (`workspace.go:192`). Replace with a lookup of the workspace's `RepoAlias`
  in `m.worktrees` to get the correct `repoPath`.

### Phase 5 — `workspace_list` and routing helpers

- [x] Add optional `repo` filter parameter to `workspace_list` tool so callers can list
  workspaces for a single repo.
- [x] `store.List()` currently takes only `includeArchived bool`. Add an optional
  `repoAlias string` filter parameter (empty string = all repos, preserving existing
  behaviour).
- [x] Update `Manager.List()` to forward the alias filter.

### Phase 6 — `Reconcile` update

- [x] `workspace.Manager.Reconcile()` currently calls `m.tmux.ListSessions(m.cfg.SessionPrefix)`.
  The session prefix is global (not per-repo), so reconcile logic is already repo-agnostic.
  Verify this still holds and add a comment noting it.
- [x] Remove the reference to `m.cfg.RepoPath` if any lingers after Phase 2; run `grep -r
  "cfg.RepoPath" internal/` to confirm it is gone.

### Phase 7 — Backward compatibility

- [x] Confirm that an existing config file with only `repoPath`/`worktreeRoot` (no `repos`)
  still starts the binary without error (the shim from Phase 1 covers this).
- [x] Confirm that `HARNESS_REPO_PATH` env var still works (shim should pick it up).
- [x] Write a `TestLegacyConfigShim` in `internal/config/config_test.go` asserting that
  a config with `repoPath: "/some/repo"` produces `cfg.Repos["default"].Path == "/some/repo"`.
- [x] Keep `Config.RepoPath` and `Config.WorktreeRoot` fields present but mark them
  `// Deprecated: use Repos` in a doc comment. Do not remove in this phase.

### Phase 8 — Integration test

- [x] Add `//go:build integration` test in `internal/workspace/workspace_integration_test.go`
  that creates two temp git repos, builds a two-repo config, starts a `Manager`, and
  verifies `workspace_create` with each alias produces a worktree under the correct
  `WorktreeRoot`.
- [x] Confirm `workspace_list` with a `repo` filter returns only workspaces for that repo.
- [x] Confirm `workspace_delete` removes the branch from the correct repo.

### Phase 9 — Docs and MCP config example

- [x] Update `README.md` with a multi-repo `config.json` example showing the `repos` object.
- [x] Update `mcp-config.example.json` (if it exists at repo root) with the new `repos` key.
- [x] Document the `HARNESS_REPOS` env var format.

---

## Config file shape (Approach A)

```json
{
  "repos": {
    "articulant": {
      "path": "/Users/me/github/articulant/main",
      "worktreeRoot": "/Users/me/github/articulant/worktrees"
    },
    "client-app": {
      "path": "/Users/me/github/client-app"
    }
  },
  "storePath": "~/.config/tmux-harness/workspaces.json",
  "claudeCmd": "claude",
  "maxWorkspaces": 20
}
```

The `worktreeRoot` field inside each repo entry is optional and defaults to
`filepath.Dir(path) + "/worktrees"` — the same convention the single-repo config uses today.

---

## Open questions (not blocking the task list)

- Should `maxWorkspaces` be a global cap or per-repo? (Current implementation is global.)
- Should `workspace_create` accept a full `repo_path` as a fallback if the alias is not in
  the config (Approach B escape hatch)? Deferring for now.
- Session-prefix collisions: if two repos have a workspace named `feat-x`, their tmux
  sessions are both `harness-feat-x`. Consider prefixing sessions with the repo alias
  (e.g. `harness-articulant-feat-x`). This is a breaking change to session naming.
