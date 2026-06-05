package idle

import (
	"context"
	"testing"
	"time"

	"github.com/articulant/tmux-harness/internal/store"
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

// mockUpdater records calls; optionally mutates the workspace in-place (stored externally).
type mockUpdater struct {
	applied []func(*store.Workspace)
	err     error
}

func (m *mockUpdater) Update(_ string, apply func(*store.Workspace)) error {
	if m.err != nil {
		return m.err
	}
	m.applied = append(m.applied, apply)
	return nil
}

func newWS(lastHash string, lastChanged time.Time) store.Workspace {
	return store.Workspace{
		ID:              "ws-1",
		Name:            "test",
		TmuxSession:     "harness-test",
		Status:          store.StatusActive,
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
	assert.Len(t, upd.applied, 1, "update should have been called")
}

func TestCheck_HashChanged_Busy(t *testing.T) {
	cap := &mockCapture{content: "new output\n"}
	upd := &mockUpdater{}
	ws := newWS(hashContent("old output\n"), time.Now().Add(-10*time.Second))

	status, err := Check(context.Background(), ws, cap, upd, 5000)
	require.NoError(t, err)
	assert.False(t, status.Idle)
	assert.Len(t, upd.applied, 1, "update should have been called on hash change")
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
	assert.Empty(t, upd.applied, "no update expected when hash unchanged")
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
	assert.Empty(t, upd.applied)
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
