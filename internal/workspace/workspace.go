// Package workspace combines the tmux, worktree, and store packages into a high-level
// coordinator. MCP tool handlers call only this package — never the sub-packages directly.
package workspace

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"

	"github.com/articulant/tmux-harness/internal/config"
	"github.com/articulant/tmux-harness/internal/store"
	"github.com/articulant/tmux-harness/internal/tmux"
	"github.com/articulant/tmux-harness/internal/worktree"
)

var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,38}[a-z0-9]$|^[a-z0-9]$`)

var reservedNames = map[string]bool{"new": true, "list": true, "delete": true}

// CreateOptions holds parameters for creating a workspace.
type CreateOptions struct {
	Name   string
	Branch string
	Meta   map[string]string
}

// Manager is the high-level workspace coordinator.
type Manager struct {
	tmux     *tmux.Client
	worktree *worktree.Client
	store    *store.Store
	cfg      *config.Config
}

// New constructs a Manager.
func New(t *tmux.Client, wt *worktree.Client, s *store.Store, cfg *config.Config) *Manager {
	return &Manager{tmux: t, worktree: wt, store: s, cfg: cfg}
}

// Create creates a new workspace: git worktree + tmux session + Claude Code instance.
// On partial failure each completed step is rolled back before returning.
func (m *Manager) Create(ctx context.Context, opts CreateOptions) (store.Workspace, error) {
	if err := validateName(opts.Name); err != nil {
		return store.Workspace{}, err
	}

	// Name conflict check.
	existing := m.store.List(false)
	for _, ws := range existing {
		if ws.Name == opts.Name {
			return store.Workspace{}, fmt.Errorf("%w: %s", ErrInvalidName, opts.Name+" already exists")
		}
	}

	// Capacity check.
	if len(existing) >= m.cfg.MaxWorkspaces {
		return store.Workspace{}, fmt.Errorf("%w: limit is %d", ErrCapacityReached, m.cfg.MaxWorkspaces)
	}

	branch := opts.Branch
	if branch == "" {
		branch = opts.Name
	}

	sessionName := m.cfg.SessionPrefix + opts.Name
	worktreePath := filepath.Join(m.cfg.WorktreeRoot, opts.Name)

	// Step 1: create worktree.
	if err := m.worktree.Add(worktreePath, branch, true); err != nil {
		return store.Workspace{}, fmt.Errorf("creating worktree: %w", err)
	}

	// Step 2: create tmux session. On failure, remove the worktree.
	if err := m.tmux.NewSession(sessionName, worktreePath); err != nil {
		_ = m.worktree.Remove(worktreePath, true)
		return store.Workspace{}, fmt.Errorf("creating tmux session: %w", err)
	}

	// Wait 300 ms for tmux to settle before sending keys.
	// tmux sessions need a moment to initialise the shell before accepting input.
	time.Sleep(300 * time.Millisecond)

	// Step 3: launch Claude Code inside the session.
	if err := m.tmux.SendKeys(sessionName, m.cfg.ClaudeCmd, true); err != nil {
		_ = m.tmux.KillSession(sessionName)
		_ = m.worktree.Remove(worktreePath, true)
		return store.Workspace{}, fmt.Errorf("launching claude: %w", err)
	}

	// Step 4: register in store.
	now := time.Now()
	ws := store.Workspace{
		Name:          opts.Name,
		TmuxSession:   sessionName,
		WorktreePath:  worktreePath,
		Branch:        branch,
		Status:        store.StatusActive,
		CreatedAt:     now,
		LastChangedAt: now,
		Meta:          opts.Meta,
	}
	if err := m.store.Add(ws); err != nil {
		_ = m.tmux.KillSession(sessionName)
		_ = m.worktree.Remove(worktreePath, true)
		return store.Workspace{}, fmt.Errorf("registering workspace: %w", err)
	}

	// Reload to get the store-assigned ID.
	created, err := m.store.GetByName(opts.Name)
	if err != nil {
		return store.Workspace{}, fmt.Errorf("reloading workspace after create: %w", err)
	}
	return created, nil
}

// Archive gracefully shuts down a workspace: exits Claude, removes the worktree,
// retains the git branch, and sets status to archived.
func (m *Manager) Archive(ctx context.Context, id string) (store.Workspace, error) {
	ws, err := m.store.Get(id)
	if err != nil {
		return store.Workspace{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if ws.Status != store.StatusActive {
		return store.Workspace{}, fmt.Errorf("%w: %s", ErrAlreadyArchived, ws.Name)
	}

	// Ask Claude to exit gracefully.
	_ = m.tmux.SendKeys(ws.TmuxSession, "exit", true)

	// Poll until the session is gone or 5 s elapses.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		exists, err := m.tmux.SessionExists(m.cfg.SessionPrefix, ws.Name)
		if err != nil || !exists {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Force-kill if still alive.
	_ = m.tmux.KillSession(ws.TmuxSession)

	// Remove the worktree. Try clean first, then force if dirty.
	if err := m.worktree.Remove(ws.WorktreePath, false); err != nil {
		fmt.Fprintf(os.Stderr, "worktree remove failed, retrying with --force: %v\n", err)
		_ = m.worktree.Remove(ws.WorktreePath, true)
	}

	// Update store.
	now := time.Now()
	if err := m.store.Update(id, func(w *store.Workspace) {
		w.Status = store.StatusArchived
		w.ArchivedAt = &now
	}); err != nil {
		return store.Workspace{}, fmt.Errorf("updating store: %w", err)
	}

	return m.store.Get(id)
}

// Delete archives the workspace and also deletes the git branch.
// confirmed must be true — if false, returns ErrDeleteNotConfirmed without doing anything.
//
// WARNING: This is the only operation that deletes a git branch. It cannot be undone.
func (m *Manager) Delete(ctx context.Context, id string, confirmed bool) error {
	if !confirmed {
		return ErrDeleteNotConfirmed
	}

	ws, err := m.store.Get(id)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}

	// Archive first (exits session, removes worktree).
	if ws.Status == store.StatusActive {
		if _, err := m.Archive(ctx, id); err != nil {
			return fmt.Errorf("archiving before delete: %w", err)
		}
		// Re-fetch after archive.
		ws, err = m.store.Get(id)
		if err != nil {
			return fmt.Errorf("re-fetching workspace: %w", err)
		}
	}

	// Delete the git branch.
	out, err := exec.Command("git", "-C", m.cfg.RepoPath, "branch", "-d", ws.Branch).Output() //nolint:gosec
	if err != nil {
		// Try force-delete.
		out2, err2 := exec.Command("git", "-C", m.cfg.RepoPath, "branch", "-D", ws.Branch).Output() //nolint:gosec
		if err2 != nil {
			fmt.Fprintf(os.Stderr, "failed to delete branch %q: %v (output: %s %s)\n",
				ws.Branch, err2, out, out2)
		}
	}

	return m.store.Delete(id)
}

// List returns workspaces. If includeArchived is false, only active workspaces are returned.
func (m *Manager) List(includeArchived bool) []store.Workspace {
	return m.store.List(includeArchived)
}

// Get returns a workspace by ID.
func (m *Manager) Get(id string) (store.Workspace, error) {
	ws, err := m.store.Get(id)
	if err != nil {
		return store.Workspace{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return ws, nil
}

// GetByName returns a workspace by name.
func (m *Manager) GetByName(name string) (store.Workspace, error) {
	ws, err := m.store.GetByName(name)
	if err != nil {
		return store.Workspace{}, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	return ws, nil
}

// SendKeys sends text to the workspace's tmux session.
func (m *Manager) SendKeys(id string, text string, pressEnter bool) error {
	ws, err := m.store.Get(id)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return m.tmux.SendKeys(ws.TmuxSession, text, pressEnter)
}

// Reconcile checks all active workspaces against live tmux sessions and marks missing
// ones as orphaned. Called once at startup.
func (m *Manager) Reconcile(ctx context.Context) error {
	active := m.store.List(false)

	liveSessions, err := m.tmux.ListSessions(m.cfg.SessionPrefix)
	if err != nil {
		return fmt.Errorf("listing tmux sessions: %w", err)
	}

	liveSet := make(map[string]bool, len(liveSessions))
	for _, s := range liveSessions {
		liveSet[s] = true
	}

	// Check active workspaces against live sessions.
	for _, ws := range active {
		if !liveSet[ws.TmuxSession] {
			fmt.Fprintf(os.Stderr, "reconcile: workspace %q session %q not found — marking orphaned\n",
				ws.Name, ws.TmuxSession)
			if err := m.store.Update(ws.ID, func(w *store.Workspace) {
				w.Status = store.StatusOrphaned
			}); err != nil {
				fmt.Fprintf(os.Stderr, "reconcile: failed to update %q: %v\n", ws.Name, err)
			}
			delete(liveSet, ws.TmuxSession)
		} else {
			delete(liveSet, ws.TmuxSession)
		}
	}

	// Warn about sessions not in the store.
	for session := range liveSet {
		fmt.Fprintf(os.Stderr, "reconcile: tmux session %q has no store entry — possibly created manually\n", session)
	}

	return nil
}

func validateName(name string) error {
	if reservedNames[name] {
		return fmt.Errorf("%w: %q is reserved", ErrInvalidName, name)
	}
	if !validName.MatchString(name) {
		return fmt.Errorf("%w: %q must be 1-40 lowercase alphanumeric characters or hyphens", ErrInvalidName, name)
	}
	return nil
}
