package worktree

import (
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockExecutor records the last call and returns a preset response.
type mockExecutor struct {
	out      []byte
	err      error
	lastRepo string
	lastCmd  string
	lastArgs []string
}

func (m *mockExecutor) Run(repoPath, name string, args ...string) ([]byte, error) {
	m.lastRepo = repoPath
	m.lastCmd = name
	m.lastArgs = args
	return m.out, m.err
}

// loadFixture reads the porcelain fixture file from the test fixtures directory.
func loadFixture(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("../../test/fixtures/worktree_list_porcelain.txt")
	require.NoError(t, err)
	return string(data)
}

func TestParsePorcelain_Fixture(t *testing.T) {
	input := loadFixture(t)
	got := parsePorcelain(input)
	require.Len(t, got, 5)

	// Main worktree.
	assert.Equal(t, "/home/user/repo", got[0].Path)
	assert.Equal(t, "main", got[0].Branch)
	assert.Equal(t, "abc1234567890abcdef1234567890abcdef123456", got[0].Head)
	assert.False(t, got[0].Locked)
	assert.False(t, got[0].Prunable)

	// Regular branch worktree.
	assert.Equal(t, "/home/user/worktrees/feature-branch", got[1].Path)
	assert.Equal(t, "feature-branch", got[1].Branch)
	assert.False(t, got[1].Locked)
	assert.False(t, got[1].Prunable)

	// Detached HEAD — Branch must be empty.
	assert.Equal(t, "/home/user/worktrees/detached-head", got[2].Path)
	assert.Equal(t, "", got[2].Branch)
	assert.Equal(t, "9876543210abcdef1234567890abcdef12345678", got[2].Head)

	// Locked worktree.
	assert.Equal(t, "/home/user/worktrees/locked-worktree", got[3].Path)
	assert.True(t, got[3].Locked)
	assert.False(t, got[3].Prunable)

	// Prunable worktree.
	assert.Equal(t, "/home/user/worktrees/prunable-worktree", got[4].Path)
	assert.False(t, got[4].Locked)
	assert.True(t, got[4].Prunable)
}

func TestParsePorcelain_Empty(t *testing.T) {
	got := parsePorcelain("")
	assert.Empty(t, got)
}

func TestAdd_NewBranch(t *testing.T) {
	m := &mockExecutor{}
	c := NewWithExecutor("/repo", m)
	err := c.Add("/repo/wt/feat", "feat", true)
	require.NoError(t, err)
	assert.Equal(t, "/repo", m.lastRepo)
	assert.Equal(t, []string{"worktree", "add", "/repo/wt/feat", "-b", "feat"}, m.lastArgs)
}

func TestAdd_ExistingBranch(t *testing.T) {
	m := &mockExecutor{}
	c := NewWithExecutor("/repo", m)
	err := c.Add("/repo/wt/feat", "feat", false)
	require.NoError(t, err)
	assert.Equal(t, []string{"worktree", "add", "/repo/wt/feat", "feat"}, m.lastArgs)
}

func TestAdd_PathExists(t *testing.T) {
	m := &mockExecutor{
		out: []byte("fatal: '/repo/wt/feat' already exists"),
		err: errors.New("exit status 128"),
	}
	c := NewWithExecutor("/repo", m)
	err := c.Add("/repo/wt/feat", "feat", true)
	assert.ErrorIs(t, err, ErrWorktreePathExists)
}

func TestAdd_OtherError(t *testing.T) {
	m := &mockExecutor{out: []byte("unknown error"), err: errors.New("exit status 1")}
	c := NewWithExecutor("/repo", m)
	err := c.Add("/repo/wt/feat", "feat", true)
	assert.Error(t, err)
	assert.NotErrorIs(t, err, ErrWorktreePathExists)
}

func TestRemove_Happy(t *testing.T) {
	m := &mockExecutor{}
	c := NewWithExecutor("/repo", m)
	err := c.Remove("/repo/wt/feat", false)
	require.NoError(t, err)
	assert.Equal(t, []string{"worktree", "remove", "/repo/wt/feat"}, m.lastArgs)
}

func TestRemove_Force(t *testing.T) {
	m := &mockExecutor{}
	c := NewWithExecutor("/repo", m)
	err := c.Remove("/repo/wt/feat", true)
	require.NoError(t, err)
	assert.Equal(t, []string{"worktree", "remove", "/repo/wt/feat", "--force"}, m.lastArgs)
}

func TestRemove_Error(t *testing.T) {
	m := &mockExecutor{out: []byte("fatal: not a worktree"), err: errors.New("exit status 128")}
	c := NewWithExecutor("/repo", m)
	err := c.Remove("/repo/wt/feat", false)
	assert.Error(t, err)
}

func TestList_Happy(t *testing.T) {
	fixture := loadFixture(t)
	m := &mockExecutor{out: []byte(fixture)}
	c := NewWithExecutor("/repo", m)
	wts, err := c.List()
	require.NoError(t, err)
	assert.Len(t, wts, 5)
	assert.Equal(t, []string{"worktree", "list", "--porcelain"}, m.lastArgs)
}

func TestList_Error(t *testing.T) {
	m := &mockExecutor{out: []byte("fatal error"), err: errors.New("exit status 128")}
	c := NewWithExecutor("/repo", m)
	_, err := c.List()
	assert.Error(t, err)
}

func TestPrune_Happy(t *testing.T) {
	m := &mockExecutor{}
	c := NewWithExecutor("/repo", m)
	err := c.Prune()
	require.NoError(t, err)
	assert.Equal(t, []string{"worktree", "prune"}, m.lastArgs)
}

func TestPrune_Error(t *testing.T) {
	m := &mockExecutor{out: []byte("fatal"), err: errors.New("exit status 1")}
	c := NewWithExecutor("/repo", m)
	err := c.Prune()
	assert.Error(t, err)
}
