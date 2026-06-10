package idle

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockCapture returns a fixed pane string or error.
type mockCapture struct {
	content string
	err     error
}

func (m *mockCapture) CapturePane(_ string, _ int) (string, error) {
	return m.content, m.err
}

// mockUpdater records UpdateIdleState calls.
type mockUpdater struct {
	calls []struct {
		id        string
		hash      string
		changedAt time.Time
	}
	err error
}

func (m *mockUpdater) UpdateIdleState(id, hash string, changedAt time.Time) error {
	if m.err != nil {
		return m.err
	}
	m.calls = append(m.calls, struct {
		id        string
		hash      string
		changedAt time.Time
	}{id, hash, changedAt})
	return nil
}

func newWS(lastHash string, lastChanged time.Time) WorkspaceState {
	return WorkspaceState{
		ID:              "ws-1",
		Name:            "test",
		TmuxSession:     "harness-test",
		LastCaptureHash: lastHash,
		LastChangedAt:   lastChanged,
	}
}

func TestCheck_FirstCall_AlwaysBusy(t *testing.T) {
	// Hash is empty on first call → treated as a change → busy.
	cap := &mockCapture{content: "some output\n"}
	upd := &mockUpdater{}
	ws := newWS("", time.Now().Add(-10*time.Second))

	status, err := Check(context.Background(), ws, cap, upd, 5000)
	require.NoError(t, err)
	assert.False(t, status.Idle)
	assert.Len(t, upd.calls, 1, "UpdateIdleState should have been called")
}

func TestCheck_HashChanged_Busy(t *testing.T) {
	cap := &mockCapture{content: "new output\n"}
	upd := &mockUpdater{}
	ws := newWS(hashContent("old output\n"), time.Now().Add(-10*time.Second))

	status, err := Check(context.Background(), ws, cap, upd, 5000)
	require.NoError(t, err)
	assert.False(t, status.Idle)
	assert.Len(t, upd.calls, 1, "UpdateIdleState should have been called on hash change")
}

func TestCheck_HashSame_BelowThreshold_Busy(t *testing.T) {
	content := "stable output\n"
	h := hashContent(content)
	cap := &mockCapture{content: content}
	upd := &mockUpdater{}
	// Changed 1 second ago, threshold is 5000 ms → not idle.
	ws := newWS(h, time.Now().Add(-1*time.Second))

	status, err := Check(context.Background(), ws, cap, upd, 5000)
	require.NoError(t, err)
	assert.False(t, status.Idle)
	assert.Empty(t, upd.calls, "no update expected when hash unchanged")
}

func TestCheck_HashSame_AboveThreshold_Idle(t *testing.T) {
	content := "stable output\n"
	h := hashContent(content)
	cap := &mockCapture{content: content}
	upd := &mockUpdater{}
	// Changed 10 seconds ago, threshold is 5000 ms → idle.
	ws := newWS(h, time.Now().Add(-10*time.Second))

	status, err := Check(context.Background(), ws, cap, upd, 5000)
	require.NoError(t, err)
	assert.True(t, status.Idle)
	assert.Equal(t, int64(5000), status.ThresholdMs)
	assert.Empty(t, upd.calls)
}

func TestCheck_CaptureError(t *testing.T) {
	cap := &mockCapture{err: assert.AnError}
	upd := &mockUpdater{}
	ws := newWS("", time.Now())

	_, err := Check(context.Background(), ws, cap, upd, 5000)
	assert.Error(t, err)
}

func TestCheck_UpdateError(t *testing.T) {
	cap := &mockCapture{content: "new\n"}
	upd := &mockUpdater{err: assert.AnError}
	ws := newWS("different-hash", time.Now())

	_, err := Check(context.Background(), ws, cap, upd, 5000)
	assert.Error(t, err)
}

func TestLooksIdle(t *testing.T) {
	assert.True(t, looksIdle("doing stuff\n> ", "> "))
	assert.False(t, looksIdle("doing stuff\nprocessing...", "> "))
	assert.False(t, looksIdle("", "> "))
}

func TestCheckWithPromptHeuristic_TiebreakerIdle(t *testing.T) {
	// Hash stable, elapsed is at 85% of threshold, but prompt looks idle → idle.
	content := "some work done\n> "
	h := hashContent(content)
	cap := &mockCapture{content: content}
	upd := &mockUpdater{}
	thresholdMs := int64(5000)
	// 85% of 5000ms = 4250ms elapsed.
	ws := newWS(h, time.Now().Add(-4250*time.Millisecond))

	status, err := CheckWithPromptHeuristic(context.Background(), ws, cap, upd, thresholdMs, "> ")
	require.NoError(t, err)
	assert.True(t, status.Idle)
}

func TestCheckWithPromptHeuristic_TiebreakerBusy(t *testing.T) {
	// Hash stable, elapsed is at 85% of threshold, prompt does NOT look idle → not idle.
	content := "processing..."
	h := hashContent(content)
	cap := &mockCapture{content: content}
	upd := &mockUpdater{}
	thresholdMs := int64(5000)
	ws := newWS(h, time.Now().Add(-4250*time.Millisecond))

	status, err := CheckWithPromptHeuristic(context.Background(), ws, cap, upd, thresholdMs, "> ")
	require.NoError(t, err)
	assert.False(t, status.Idle)
}

// captureEntry is the per-session result used by multiCapture.
type captureEntry struct {
	content string
	err     error
}

// multiCapture returns per-session content or error.
type multiCapture struct {
	results map[string]captureEntry
}

func (m *multiCapture) CapturePane(session string, _ int) (string, error) {
	if r, ok := m.results[session]; ok {
		return r.content, r.err
	}
	return "", nil
}

// seqCapture returns per-session content in sequence; the last element repeats.
type seqCapture struct {
	mu      sync.Mutex
	seqs    map[string][]string
	indices map[string]int
}

func (s *seqCapture) CapturePane(session string, _ int) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seq := s.seqs[session]
	if len(seq) == 0 {
		return "", nil
	}
	idx := s.indices[session]
	if idx >= len(seq) {
		idx = len(seq) - 1
	}
	s.indices[session] = idx + 1
	return seq[idx], nil
}

func TestIsIdle_Empty(t *testing.T) {
	result := IsIdle(context.Background(), nil, &mockCapture{}, 100, 10)
	assert.Empty(t, result)
}

func TestIsIdle_StableHash_ReturnsIdle(t *testing.T) {
	ws := WorkspaceState{ID: "ws-1", Name: "myws", TmuxSession: "s1"}
	cap := &mockCapture{content: "stable\n"}

	result := IsIdle(context.Background(), []WorkspaceState{ws}, cap, 100, 10)

	require.Contains(t, result, "ws-1")
	assert.True(t, result["ws-1"].Idle)
}

func TestIsIdle_ChangingHash_ReturnsBusy(t *testing.T) {
	ws := WorkspaceState{ID: "ws-1", Name: "myws", TmuxSession: "s1"}
	cap := &toggleCapture{toggle: [2]string{"a\n", "b\n"}, stableN: 1000}

	result := IsIdle(context.Background(), []WorkspaceState{ws}, cap, 100, 10)

	require.Contains(t, result, "ws-1")
	assert.False(t, result["ws-1"].Idle)
}

func TestIsIdle_ErrorOmitted(t *testing.T) {
	ws1 := WorkspaceState{ID: "ws-1", Name: "good", TmuxSession: "s1"}
	ws2 := WorkspaceState{ID: "ws-2", Name: "bad", TmuxSession: "s2"}

	cap := &multiCapture{results: map[string]captureEntry{
		"s1": {content: "stable\n"},
		"s2": {err: assert.AnError},
	}}

	result := IsIdle(context.Background(), []WorkspaceState{ws1, ws2}, cap, 100, 10)

	require.Contains(t, result, "ws-1")
	assert.True(t, result["ws-1"].Idle)
	assert.NotContains(t, result, "ws-2")
}

func TestIsIdle_MultipleWorkspaces(t *testing.T) {
	wsIdle := WorkspaceState{ID: "idle", Name: "idle", TmuxSession: "s-idle"}
	wsBusy := WorkspaceState{ID: "busy", Name: "busy", TmuxSession: "s-busy"}

	// s-idle always returns the same content; s-busy changes on the second call.
	cap := &seqCapture{
		seqs: map[string][]string{
			"s-idle": {"stable\n"},
			"s-busy": {"first\n", "second\n"},
		},
		indices: make(map[string]int),
	}

	result := IsIdle(context.Background(), []WorkspaceState{wsIdle, wsBusy}, cap, 100, 10)

	require.Contains(t, result, "idle")
	assert.True(t, result["idle"].Idle)
	require.Contains(t, result, "busy")
	assert.False(t, result["busy"].Idle)
}

// toggleCapture returns alternating content on each call, then a fixed stable string.
type toggleCapture struct {
	calls   int
	toggle  [2]string
	stableN int // after this many calls, always return toggle[1]
}

func (t *toggleCapture) CapturePane(_ string, _ int) (string, error) {
	t.calls++
	if t.calls > t.stableN {
		return t.toggle[1], nil
	}
	return t.toggle[t.calls%2], nil
}

func TestWaitUntilIdle_AlreadyIdle(t *testing.T) {
	content := "stable output\n"
	h := hashContent(content)
	cap := &mockCapture{content: content}
	upd := &mockUpdater{}
	// Already past threshold.
	ws := newWS(h, time.Now().Add(-10*time.Second))

	status, err := WaitUntilIdle(context.Background(), ws, cap, upd, 200, 5000, 50)
	require.NoError(t, err)
	assert.True(t, status.Idle)
}

func TestWaitUntilIdle_BecomesIdleAfterTicks(t *testing.T) {
	content := "stable\n"
	h := hashContent(content)
	cap := &mockCapture{content: content}
	upd := &mockUpdater{}
	// Not yet past threshold — needs ~3 polls at 50 ms each to accumulate 150 ms.
	ws := newWS(h, time.Now().Add(-50*time.Millisecond))

	start := time.Now()
	status, err := WaitUntilIdle(context.Background(), ws, cap, upd, 150, 5000, 50)
	require.NoError(t, err)
	assert.True(t, status.Idle)
	// Should have waited at least one poll interval.
	assert.GreaterOrEqual(t, time.Since(start).Milliseconds(), int64(50))
}

func TestWaitUntilIdle_Timeout(t *testing.T) {
	// Content keeps changing → never idle.
	cap := &toggleCapture{toggle: [2]string{"a\n", "b\n"}, stableN: 1000}
	upd := &mockUpdater{}
	ws := newWS("", time.Now())

	_, err := WaitUntilIdle(context.Background(), ws, cap, upd, 200, 120, 50)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestWaitUntilIdleMulti_AllMode_BothIdle(t *testing.T) {
	content := "stable\n"
	h := hashContent(content)
	ws1 := WorkspaceState{
		ID: "ws-1", Name: "myws1", TmuxSession: "s1",
		LastCaptureHash: h, LastChangedAt: time.Now().Add(-10 * time.Second),
	}
	ws2 := WorkspaceState{
		ID: "ws-2", Name: "myws2", TmuxSession: "s2",
		LastCaptureHash: h, LastChangedAt: time.Now().Add(-10 * time.Second),
	}
	cap := &multiCapture{results: map[string]captureEntry{
		"s1": {content: content},
		"s2": {content: content},
	}}
	upd := &mockUpdater{}

	results, timedOut := WaitUntilIdleMulti(context.Background(), []WorkspaceState{ws1, ws2}, cap, upd, 5000, 1000, "all")
	assert.False(t, timedOut)
	assert.True(t, results["ws-1"])
	assert.True(t, results["ws-2"])
}

func TestWaitUntilIdleMulti_AnyMode_OneIdle(t *testing.T) {
	idleContent := "stable\n"
	idleHash := hashContent(idleContent)
	ws1 := WorkspaceState{
		ID: "ws-1", Name: "idle", TmuxSession: "s1",
		LastCaptureHash: idleHash, LastChangedAt: time.Now().Add(-10 * time.Second),
	}
	ws2 := WorkspaceState{
		// No prior hash — first check records hash, elapsed < threshold → not idle.
		ID: "ws-2", Name: "busy", TmuxSession: "s2",
	}
	cap := &multiCapture{results: map[string]captureEntry{
		"s1": {content: idleContent},
		"s2": {content: "active\n"},
	}}
	upd := &mockUpdater{}

	results, timedOut := WaitUntilIdleMulti(context.Background(), []WorkspaceState{ws1, ws2}, cap, upd, 5000, 1000, "any")
	assert.False(t, timedOut)
	assert.True(t, results["ws-1"])
	assert.False(t, results["ws-2"])
}

func TestWaitUntilIdleMulti_Timeout(t *testing.T) {
	// Content keeps changing → never idle; timeout fires before first 500 ms tick.
	ws := WorkspaceState{ID: "ws-1", Name: "myws", TmuxSession: "s1"}
	cap := &toggleCapture{toggle: [2]string{"a\n", "b\n"}, stableN: 1000}
	upd := &mockUpdater{}

	results, timedOut := WaitUntilIdleMulti(context.Background(), []WorkspaceState{ws}, cap, upd, 200, 120, "all")
	assert.True(t, timedOut)
	assert.False(t, results["ws-1"])
}

func TestWaitUntilIdle_CtxCancellation(t *testing.T) {
	cap := &toggleCapture{toggle: [2]string{"a\n", "b\n"}, stableN: 1000}
	upd := &mockUpdater{}
	ws := newWS("", time.Now())

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(80 * time.Millisecond)
		cancel()
	}()

	_, err := WaitUntilIdle(ctx, ws, cap, upd, 200, 60_000, 50)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}
