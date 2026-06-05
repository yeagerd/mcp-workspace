//go:build integration

package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/articulant/tmux-harness/internal/config"
	"github.com/articulant/tmux-harness/internal/store"
	"github.com/articulant/tmux-harness/internal/tmux"
	"github.com/articulant/tmux-harness/internal/worktree"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func skipIfNoIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("HARNESS_INTEGRATION") != "1" {
		t.Skip("set HARNESS_INTEGRATION=1 to run integration tests")
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

func TestIntegration_CreateAndArchive(t *testing.T) {
	skipIfNoIntegration(t)

	repoPath := setupTestRepo(t)
	m := setupManager(t, repoPath)
	ctx := context.Background()

	ws, err := m.Create(ctx, CreateOptions{Name: "inttest-basic"})
	require.NoError(t, err)
	assert.Equal(t, "inttest-basic", ws.Name)
	assert.Equal(t, store.StatusActive, ws.Status)

	t.Cleanup(func() {
		_, _ = m.Archive(ctx, ws.ID)
	})

	// Session should exist.
	exists, err := m.tmux.SessionExists(m.cfg.SessionPrefix, "inttest-basic")
	require.NoError(t, err)
	assert.True(t, exists)

	// Archive.
	archived, err := m.Archive(ctx, ws.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusArchived, archived.Status)
	assert.NotNil(t, archived.ArchivedAt)

	// Session should be gone.
	exists, err = m.tmux.SessionExists(m.cfg.SessionPrefix, "inttest-basic")
	require.NoError(t, err)
	assert.False(t, exists)
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
		_, _ = m.Archive(ctx, ws.ID)
	})

	// Kill session directly to simulate an orphan.
	require.NoError(t, m.tmux.KillSession(ws.TmuxSession))

	// Reconcile should mark it as orphaned.
	require.NoError(t, m.Reconcile(ctx))

	reloaded, err := m.store.Get(ws.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusOrphaned, reloaded.Status)
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
	t.Cleanup(func() { _, _ = m.Archive(ctx, ws.ID) })

	_, err = m.Create(ctx, CreateOptions{Name: "cap-second"})
	assert.ErrorIs(t, err, ErrCapacityReached)
}
