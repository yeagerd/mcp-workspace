package workspace

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yeagerd/hangar/internal/config"
	"github.com/yeagerd/hangar/internal/store"
	"github.com/yeagerd/hangar/internal/tmux"
	"github.com/yeagerd/hangar/internal/worktree"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockWorktree implements worktreeClient for unit tests.
type mockWorktree struct {
	dirty    bool
	unpushed bool
	checkErr error
}

func (m *mockWorktree) Add(_, _ string, _ bool, _ string) error           { return nil }
func (m *mockWorktree) Remove(_ string, _ bool) error                     { return nil }
func (m *mockWorktree) FindByPath(_ string) (worktree.WorktreeInfo, bool) { return worktree.WorktreeInfo{}, false }
func (m *mockWorktree) CheckClean(_, _ string) (bool, bool, error) {
	return m.dirty, m.unpushed, m.checkErr
}

func makeDeleteManager(t *testing.T, wt worktreeClient) *Manager {
	t.Helper()
	dir := t.TempDir()
	storePath := filepath.Join(dir, "ws.json")
	s, err := store.NewStore(storePath)
	require.NoError(t, err)
	cfg := &config.Config{
		WorktreeRoot:  dir,
		SessionPrefix: "test-",
		RepoPath:      dir,
		MaxWorkspaces: 10,
	}
	return New(tmux.New(), wt, s, cfg)
}

func addWS(t *testing.T, m *Manager, name, branch string) string {
	t.Helper()
	sw := store.Workspace{
		Name: name, Branch: branch,
		CreatedAt: time.Now(), LastChangedAt: time.Now(),
	}
	require.NoError(t, m.store.Add(sw))
	reloaded, err := m.store.GetByName(name)
	require.NoError(t, err)
	return reloaded.ID
}

func TestDelete_NotConfirmed(t *testing.T) {
	m := makeDeleteManager(t, &mockWorktree{})
	id := addWS(t, m, "ws1", "ws1")
	err := m.Delete(context.Background(), id, false, false, false)
	assert.ErrorIs(t, err, ErrDeleteNotConfirmed)
}

func TestDelete_DirtyWorktree(t *testing.T) {
	wt := &mockWorktree{dirty: true}
	m := makeDeleteManager(t, wt)
	id := addWS(t, m, "ws1", "ws1")
	err := m.Delete(context.Background(), id, true, false, false)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "uncommitted changes"), "got: %s", err)
	assert.False(t, strings.Contains(err.Error(), "unpushed commits"), "got: %s", err)
}

func TestDelete_UnpushedCommits(t *testing.T) {
	wt := &mockWorktree{unpushed: true}
	m := makeDeleteManager(t, wt)
	id := addWS(t, m, "ws1", "ws1")
	err := m.Delete(context.Background(), id, true, false, false)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "unpushed commits"), "got: %s", err)
	assert.False(t, strings.Contains(err.Error(), "uncommitted changes"), "got: %s", err)
}

func TestDelete_BothDirtyAndUnpushed(t *testing.T) {
	wt := &mockWorktree{dirty: true, unpushed: true}
	m := makeDeleteManager(t, wt)
	id := addWS(t, m, "ws1", "ws1")
	err := m.Delete(context.Background(), id, true, false, false)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "uncommitted changes"), "got: %s", err)
	assert.True(t, strings.Contains(err.Error(), "unpushed commits"), "got: %s", err)
}

func TestDelete_ForceSkipsSafetyCheck(t *testing.T) {
	// dirty=true but force=true — safety check is bypassed; error will come from tmux/git
	// (no real environment), but it must NOT be a safety-check error.
	wt := &mockWorktree{dirty: true, unpushed: true}
	m := makeDeleteManager(t, wt)
	id := addWS(t, m, "ws1", "ws1")
	err := m.Delete(context.Background(), id, true, true, false)
	if err != nil {
		assert.False(t, strings.Contains(err.Error(), "uncommitted changes"), "got safety-check error with force=true: %s", err)
		assert.False(t, strings.Contains(err.Error(), "unpushed commits"), "got safety-check error with force=true: %s", err)
	}
}
