//go:build integration

package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/yeagerd/hangar/internal/config"
	"github.com/yeagerd/hangar/internal/store"
	"github.com/yeagerd/hangar/internal/tmux"
	"github.com/yeagerd/hangar/internal/worktree"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func skipIfNoIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("HANGAR_INTEGRATION") != "1" {
		t.Skip("set HANGAR_INTEGRATION=1 to run integration tests")
	}
}

// setupTestRepo initialises a git repo with an initial commit.
func setupTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	git := func(args ...string) {
		t.Helper()
		fullArgs := append([]string{"-C", dir}, args...)
		out, err := exec.Command("git", fullArgs...).CombinedOutput() //nolint:gosec
		require.NoError(t, err, "git %v: %s", args, out)
	}

	git("init")
	git("config", "user.email", "test@test.com")
	git("config", "user.name", "Test")
	readme := filepath.Join(dir, "README.md")
	require.NoError(t, os.WriteFile(readme, []byte("test"), 0o644))
	git("add", "README.md")
	git("commit", "-m", "init")

	return dir
}

func setupManager(t *testing.T, repoPath string) *Manager {
	t.Helper()
	wtRoot := t.TempDir()
	storePath := filepath.Join(t.TempDir(), "workspaces.json")

	cfg := &config.Config{
		RepoPath:        repoPath,
		WorktreeRoot:    wtRoot,
		StorePath:       storePath,
		ClaudeCmd:       "echo", // use echo instead of real claude in tests
		IdleThresholdMs: 5000,
		SessionPrefix:   "harness-inttest-",
		MaxWorkspaces:   10,
	}

	s, err := store.NewStore(storePath)
	require.NoError(t, err)

	return New(tmux.New(), worktree.New(repoPath), s, cfg)
}

func TestIntegration_CreateAndDelete(t *testing.T) {
	skipIfNoIntegration(t)

	repoPath := setupTestRepo(t)
	m := setupManager(t, repoPath)
	ctx := context.Background()

	ws, err := m.Create(ctx, CreateOptions{Name: "inttest-basic"})
	require.NoError(t, err)
	assert.Equal(t, "inttest-basic", ws.Name)
	assert.NotEmpty(t, ws.TmuxSession)

	t.Cleanup(func() {
		_ = m.Delete(ctx, ws.ID, true)
	})

	// Session should exist.
	exists, err := m.tmux.SessionExists(m.cfg.SessionPrefix, "inttest-basic")
	require.NoError(t, err)
	assert.True(t, exists)

	// Delete.
	require.NoError(t, m.Delete(ctx, ws.ID, true))

	// Session should be gone.
	exists, err = m.tmux.SessionExists(m.cfg.SessionPrefix, "inttest-basic")
	require.NoError(t, err)
	assert.False(t, exists)

	// Store entry should be gone.
	_, err = m.Get(ws.ID)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestIntegration_Reconcile(t *testing.T) {
	skipIfNoIntegration(t)

	repoPath := setupTestRepo(t)
	m := setupManager(t, repoPath)
	ctx := context.Background()

	ws, err := m.Create(ctx, CreateOptions{Name: "inttest-orphan"})
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = m.tmux.KillSession(ws.TmuxSession)
		_ = m.Delete(ctx, ws.ID, true)
	})

	// Kill session directly to simulate a missing session.
	require.NoError(t, m.tmux.KillSession(ws.TmuxSession))

	// Reconcile is read-only; it logs discrepancies but does not write to the store.
	require.NoError(t, m.Reconcile(ctx))

	// The store record is unchanged — workspace still exists.
	reloaded, err := m.Get(ws.ID)
	require.NoError(t, err)
	assert.Equal(t, ws.ID, reloaded.ID)
}

func TestIntegration_CapacityLimit(t *testing.T) {
	skipIfNoIntegration(t)

	repoPath := setupTestRepo(t)
	wtRoot := t.TempDir()
	storePath := filepath.Join(t.TempDir(), "ws.json")

	cfg := &config.Config{
		RepoPath:        repoPath,
		WorktreeRoot:    wtRoot,
		StorePath:       storePath,
		ClaudeCmd:       "echo",
		IdleThresholdMs: 5000,
		SessionPrefix:   "harness-cap-",
		MaxWorkspaces:   1,
	}

	s, err := store.NewStore(storePath)
	require.NoError(t, err)
	m := New(tmux.New(), worktree.New(repoPath), s, cfg)
	ctx := context.Background()

	ws, err := m.Create(ctx, CreateOptions{Name: "cap-first"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Delete(ctx, ws.ID, true) })

	_, err = m.Create(ctx, CreateOptions{Name: "cap-second"})
	assert.ErrorIs(t, err, ErrCapacityReached)
}
