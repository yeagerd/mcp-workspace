package workspace

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/yeagerd/hangar/internal/config"
	"github.com/yeagerd/hangar/internal/store"
	"github.com/yeagerd/hangar/internal/tmux"
	"github.com/yeagerd/hangar/internal/worktree"
)

// fakeTmuxExec returns an error for every tmux call, causing SessionExists to report false.
type fakeTmuxExec struct{}

func (f *fakeTmuxExec) Run(_ string, _ ...string) ([]byte, error) {
	return []byte("can't find session"), fmt.Errorf("exit status 1")
}

// fakeWTExec returns empty output for every git call, causing FindByPath to report no match.
type fakeWTExec struct{}

func (f *fakeWTExec) Run(_, _ string, _ ...string) ([]byte, error) {
	return []byte(""), nil
}

func newResolveManager(t *testing.T, workspaces []store.Workspace) *Manager {
	t.Helper()
	storePath := filepath.Join(t.TempDir(), "ws.json")
	s, err := store.NewStore(storePath)
	require.NoError(t, err)
	for _, ws := range workspaces {
		require.NoError(t, s.Add(ws))
	}
	cfg := &config.Config{
		WorktreeRoot:  t.TempDir(),
		SessionPrefix: "h-",
	}
	return New(
		tmux.NewWithExecutor(&fakeTmuxExec{}),
		worktree.NewWithExecutor("", &fakeWTExec{}),
		s,
		cfg,
	)
}

func archivedAt() *time.Time {
	t := time.Now()
	return &t
}

func TestResolve_ExactID(t *testing.T) {
	m := newResolveManager(t, []store.Workspace{
		{ID: "aaaa-1111", Name: "alpha", ArchivedAt: archivedAt()},
		{ID: "bbbb-2222", Name: "beta", ArchivedAt: archivedAt()},
	})
	ws, err := m.Resolve("aaaa-1111")
	require.NoError(t, err)
	assert.Equal(t, "aaaa-1111", ws.ID)
	assert.Equal(t, "alpha", ws.Name)
}

func TestResolve_ExactName(t *testing.T) {
	m := newResolveManager(t, []store.Workspace{
		{ID: "aaaa-1111", Name: "alpha", ArchivedAt: archivedAt()},
		{ID: "bbbb-2222", Name: "beta", ArchivedAt: archivedAt()},
	})
	ws, err := m.Resolve("alpha")
	require.NoError(t, err)
	assert.Equal(t, "aaaa-1111", ws.ID)
}

func TestResolve_IDPrefix(t *testing.T) {
	m := newResolveManager(t, []store.Workspace{
		{ID: "aaaa-1111", Name: "alpha", ArchivedAt: archivedAt()},
		{ID: "bbbb-2222", Name: "beta", ArchivedAt: archivedAt()},
	})
	ws, err := m.Resolve("aaaa")
	require.NoError(t, err)
	assert.Equal(t, "aaaa-1111", ws.ID)
}

func TestResolve_NamePrefix(t *testing.T) {
	m := newResolveManager(t, []store.Workspace{
		{ID: "aaaa-1111", Name: "alpha", ArchivedAt: archivedAt()},
		{ID: "bbbb-2222", Name: "beta", ArchivedAt: archivedAt()},
	})
	ws, err := m.Resolve("alp")
	require.NoError(t, err)
	assert.Equal(t, "aaaa-1111", ws.ID)
}

func TestResolve_AmbiguousPrefix(t *testing.T) {
	m := newResolveManager(t, []store.Workspace{
		{ID: "aaaa-1111", Name: "alpha", ArchivedAt: archivedAt()},
		{ID: "aaaa-2222", Name: "axle", ArchivedAt: archivedAt()},
	})
	_, err := m.Resolve("aaaa")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrAmbiguous), "expected ErrAmbiguous, got: %v", err)
}

func TestResolve_NotFound(t *testing.T) {
	m := newResolveManager(t, []store.Workspace{
		{ID: "aaaa-1111", Name: "alpha", ArchivedAt: archivedAt()},
	})
	_, err := m.Resolve("ghost")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNotFound), "expected ErrNotFound, got: %v", err)
}

func TestResolve_IncludesArchived(t *testing.T) {
	now := time.Now()
	m := newResolveManager(t, []store.Workspace{
		{ID: "aaaa-1111", Name: "alpha", ArchivedAt: &now},
	})
	ws, err := m.Resolve("alpha")
	require.NoError(t, err)
	assert.Equal(t, "aaaa-1111", ws.ID)
}

func TestResolve_DeduplicatesMultipleCriteriaMatch(t *testing.T) {
	// A workspace whose name is also a prefix of its own ID should match only once.
	m := newResolveManager(t, []store.Workspace{
		{ID: "alpha-1111", Name: "alpha", ArchivedAt: archivedAt()},
		{ID: "bbbb-2222", Name: "beta", ArchivedAt: archivedAt()},
	})
	ws, err := m.Resolve("alpha")
	require.NoError(t, err)
	assert.Equal(t, "alpha-1111", ws.ID)
}
