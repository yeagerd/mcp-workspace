// Package workspace combines the tmux, worktree, and store packages into a high-level
// coordinator. MCP tool handlers call only this package — never the sub-packages directly.
package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/yeagerd/hangar/internal/config"
	"github.com/yeagerd/hangar/internal/store"
	"github.com/yeagerd/hangar/internal/tmux"
	"github.com/yeagerd/hangar/internal/worktree"
)

var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,38}[a-z0-9]$|^[a-z0-9]$`)

var reservedNames = map[string]bool{"new": true, "list": true, "delete": true}

// worktreeClient is the subset of *worktree.Client that Manager requires.
type worktreeClient interface {
	Add(worktreePath, branchName string, createBranch bool, baseBranch string) error
	Remove(worktreePath string, force bool) error
	FindByPath(path string) (worktree.WorktreeInfo, bool)
	CheckClean(worktreePath, branch string) (dirty bool, unpushed bool, err error)
}

// CreateOptions holds parameters for creating a workspace.
type CreateOptions struct {
	Name         string
	Branch       string
	BaseBranch   string
	CreateBranch bool
	Meta         map[string]string
}

// Manager is the high-level workspace coordinator.
type Manager struct {
	tmux     *tmux.Client
	worktree worktreeClient
	store    *store.Store
	cfg      *config.Config
}

// New constructs a Manager.
func New(t *tmux.Client, wt worktreeClient, s *store.Store, cfg *config.Config) *Manager {
	return &Manager{tmux: t, worktree: wt, store: s, cfg: cfg}
}

// Create creates a new workspace: git worktree + tmux session + Claude Code instance.
// On partial failure each completed step is rolled back before returning.
func (m *Manager) Create(ctx context.Context, opts CreateOptions) (Workspace, error) {
	if err := validateName(opts.Name); err != nil {
		return Workspace{}, err
	}

	// Name conflict check.
	existing := m.store.List()
	for _, ws := range existing {
		if ws.Name == opts.Name {
			return Workspace{}, fmt.Errorf("%w: %s", ErrInvalidName, opts.Name+" already exists")
		}
	}

	// Capacity check.
	if len(existing) >= m.cfg.MaxWorkspaces {
		return Workspace{}, fmt.Errorf("%w: limit is %d", ErrCapacityReached, m.cfg.MaxWorkspaces)
	}

	branch := opts.Branch
	if branch == "" {
		branch = opts.Name
	}

	sessionName := m.cfg.SessionPrefix + opts.Name
	worktreePath := filepath.Join(m.cfg.WorktreeRoot, opts.Name)

	// Step 1: create worktree.
	if err := m.worktree.Add(worktreePath, branch, opts.CreateBranch, opts.BaseBranch); err != nil {
		return Workspace{}, fmt.Errorf("creating worktree: %w", err)
	}

	// Step 2: create tmux session. On failure, remove the worktree.
	if err := m.tmux.NewSession(sessionName, worktreePath); err != nil {
		_ = m.worktree.Remove(worktreePath, true)
		return Workspace{}, fmt.Errorf("creating tmux session: %w", err)
	}

	// Wait 300 ms for tmux to settle before sending keys.
	// tmux sessions need a moment to initialise the shell before accepting input.
	time.Sleep(300 * time.Millisecond)

	// Step 3: launch Claude Code inside the session.
	if err := m.tmux.SendKeys(sessionName, m.cfg.ClaudeCmd, true); err != nil {
		_ = m.tmux.KillSession(sessionName)
		_ = m.worktree.Remove(worktreePath, true)
		return Workspace{}, fmt.Errorf("launching claude: %w", err)
	}

	// Step 4: register in store. TmuxSession and WorktreePath are derived at query time
	// and not persisted.
	now := time.Now()
	sw := store.Workspace{
		Name:          opts.Name,
		Branch:        branch,
		CreatedAt:     now,
		LastChangedAt: now,
		Meta:          opts.Meta,
	}
	if err := m.store.Add(sw); err != nil {
		_ = m.tmux.KillSession(sessionName)
		_ = m.worktree.Remove(worktreePath, true)
		return Workspace{}, fmt.Errorf("registering workspace: %w", err)
	}

	// Reload to get the store-assigned ID, then build the combined type.
	created, err := m.store.GetByName(opts.Name)
	if err != nil {
		return Workspace{}, fmt.Errorf("reloading workspace after create: %w", err)
	}
	return m.buildWorkspace(created), nil
}

// Delete kills the workspace: exits the Claude session, removes the worktree,
// deletes the git branch, and removes the store entry.
// confirmed must be true — if false, returns ErrDeleteNotConfirmed without doing anything.
// If force is false, Delete refuses to proceed when the worktree has uncommitted changes
// or unpushed commits.
//
// WARNING: This is the only operation that deletes a git branch. It cannot be undone.
func (m *Manager) Delete(ctx context.Context, id string, confirmed bool, force bool) error {
	if !confirmed {
		return ErrDeleteNotConfirmed
	}

	sw, err := m.store.Get(id)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}

	worktreePath := filepath.Join(m.cfg.WorktreeRoot, sw.Name)

	if !force {
		dirty, unpushed, checkErr := m.worktree.CheckClean(worktreePath, sw.Branch)
		if checkErr != nil {
			return fmt.Errorf("checking workspace cleanliness: %w", checkErr)
		}
		if dirty || unpushed {
			var reasons []string
			if dirty {
				reasons = append(reasons, "workspace has uncommitted changes; commit or stash them, or pass force=true to delete anyway")
			}
			if unpushed {
				reasons = append(reasons, "workspace branch has unpushed commits; push them or pass force=true to delete anyway")
			}
			return errors.New(strings.Join(reasons, "; "))
		}
	}

	sessionName := m.cfg.SessionPrefix + sw.Name

	// Ask Claude to exit gracefully.
	_ = m.tmux.SendKeys(sessionName, "exit", true)

	// Poll until the session is gone or 5 s elapses.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		exists, err := m.tmux.SessionExists(m.cfg.SessionPrefix, sw.Name)
		if err != nil || !exists {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Force-kill if still alive.
	_ = m.tmux.KillSession(sessionName)

	// Remove the worktree. Try clean first, then force if dirty.
	if err := m.worktree.Remove(worktreePath, false); err != nil {
		fmt.Fprintf(os.Stderr, "worktree remove failed, retrying with --force: %v\n", err)
		_ = m.worktree.Remove(worktreePath, true)
	}

	// Delete the git branch.
	out, err := exec.Command("git", "-C", m.cfg.RepoPath, "branch", "-d", sw.Branch).Output() //nolint:gosec
	if err != nil {
		out2, err2 := exec.Command("git", "-C", m.cfg.RepoPath, "branch", "-D", sw.Branch).Output() //nolint:gosec
		if err2 != nil {
			fmt.Fprintf(os.Stderr, "failed to delete branch %q: %v (output: %s %s)\n",
				sw.Branch, err2, out, out2)
		}
	}

	return m.store.Delete(id)
}

// List returns all workspaces.
func (m *Manager) List() []Workspace {
	records := m.store.List()
	result := make([]Workspace, len(records))
	for i, sw := range records {
		result[i] = m.buildWorkspace(sw)
	}
	return result
}

// Get returns a workspace by ID.
func (m *Manager) Get(id string) (Workspace, error) {
	sw, err := m.store.Get(id)
	if err != nil {
		return Workspace{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return m.buildWorkspace(sw), nil
}

// GetByName returns a workspace by name.
func (m *Manager) GetByName(name string) (Workspace, error) {
	sw, err := m.store.GetByName(name)
	if err != nil {
		return Workspace{}, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	return m.buildWorkspace(sw), nil
}

// SendKeys sends text to the workspace's tmux session.
func (m *Manager) SendKeys(id string, text string, pressEnter bool) error {
	sw, err := m.store.Get(id)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	sessionName := m.cfg.SessionPrefix + sw.Name
	return m.tmux.SendKeys(sessionName, text, pressEnter)
}

// Reconcile checks all workspaces against live tmux sessions and logs discrepancies.
// Called once at startup.
func (m *Manager) Reconcile(ctx context.Context) error {
	all := m.store.List()

	liveSessions, err := m.tmux.ListSessions(m.cfg.SessionPrefix)
	if err != nil {
		return fmt.Errorf("listing tmux sessions: %w", err)
	}

	liveSet := make(map[string]bool, len(liveSessions))
	for _, s := range liveSessions {
		liveSet[s] = true
	}

	for _, ws := range all {
		sessionName := m.cfg.SessionPrefix + ws.Name
		if !liveSet[sessionName] {
			fmt.Fprintf(os.Stderr, "reconcile: workspace %q session %q not found\n",
				ws.Name, sessionName)
		}
		delete(liveSet, sessionName)
	}

	for session := range liveSet {
		fmt.Fprintf(os.Stderr, "reconcile: tmux session %q has no store entry — possibly created manually\n", session)
	}

	return nil
}

// buildWorkspace constructs a Workspace from a store record by deriving
// WorktreePath, TmuxSession, and Branch at call time from config and git.
func (m *Manager) buildWorkspace(sw store.Workspace) Workspace {
	ws := Workspace{
		ID:              sw.ID,
		Name:            sw.Name,
		CreatedAt:       sw.CreatedAt,
		LastCaptureHash: sw.LastCaptureHash,
		LastChangedAt:   sw.LastChangedAt,
		Meta:            sw.Meta,
	}

	ws.WorktreePath = filepath.Join(m.cfg.WorktreeRoot, sw.Name)
	ws.TmuxSession = m.cfg.SessionPrefix + sw.Name

	if info, ok := m.worktree.FindByPath(ws.WorktreePath); ok {
		ws.Branch = info.Branch
	} else {
		ws.Branch = sw.Branch
	}

	return ws
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
