// Package idle detects whether a Claude Code session is busy or waiting for input.
// Detection is based on hashing pane output: a stable hash over the threshold duration means idle.
package idle

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// IdleStatus is the result of a single idle check.
type IdleStatus struct {
	Idle          bool
	LastChangedAt time.Time
	ElapsedMs     int64
	ThresholdMs   int64
}

// WorkspaceState holds the fields from a workspace that idle detection requires.
type WorkspaceState struct {
	ID              string
	Name            string
	TmuxSession     string
	LastCaptureHash string
	LastChangedAt   time.Time
}

// PaneCapture abstracts the tmux capture-pane call so tests can inject fakes.
type PaneCapture interface {
	CapturePane(sessionName string, lines int) (string, error)
}

// WorkspaceUpdater abstracts persisting idle-detection state so tests can inject fakes.
type WorkspaceUpdater interface {
	UpdateIdleState(id, hash string, changedAt time.Time) error
}

// Check determines whether the workspace session is idle.
//
//  1. Capture the pane (200 lines).
//  2. SHA-256 hash the content.
//  3. If hash changed → update store, return busy.
//  4. If hash same → compute elapsed; if >= threshold, return idle.
func Check(
	ctx context.Context,
	ws WorkspaceState,
	capture PaneCapture,
	updater WorkspaceUpdater,
	thresholdMs int64,
) (IdleStatus, error) {
	content, err := capture.CapturePane(ws.TmuxSession, 200)
	if err != nil {
		return IdleStatus{}, fmt.Errorf("capturing pane for %s: %w", ws.Name, err)
	}

	hash := hashContent(content)

	if hash != ws.LastCaptureHash {
		now := time.Now()
		if err := updater.UpdateIdleState(ws.ID, hash, now); err != nil {
			return IdleStatus{}, fmt.Errorf("updating workspace hash: %w", err)
		}
		return IdleStatus{
			Idle:          false,
			LastChangedAt: now,
			ElapsedMs:     0,
			ThresholdMs:   thresholdMs,
		}, nil
	}

	elapsed := time.Since(ws.LastChangedAt).Milliseconds()
	isIdle := elapsed >= thresholdMs

	return IdleStatus{
		Idle:          isIdle,
		LastChangedAt: ws.LastChangedAt,
		ElapsedMs:     elapsed,
		ThresholdMs:   thresholdMs,
	}, nil
}

// WaitUntilIdle polls the workspace pane until it becomes idle or the timeout elapses.
// pollIntervalMs ≤ 0 defaults to 500 ms. timeoutMs ≤ 0 defaults to 600 000 ms (10 min).
// Returns a non-nil error (wrapping context.DeadlineExceeded) on timeout or ctx cancellation.
func WaitUntilIdle(
	ctx context.Context,
	ws WorkspaceState,
	capture PaneCapture,
	updater WorkspaceUpdater,
	thresholdMs, timeoutMs, pollIntervalMs int64,
) (IdleStatus, error) {
	if timeoutMs <= 0 {
		timeoutMs = 600_000
	}
	if pollIntervalMs <= 0 {
		pollIntervalMs = 500
	}

	deadline := time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	ticker := time.NewTicker(time.Duration(pollIntervalMs) * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return IdleStatus{
				Idle:          false,
				LastChangedAt: ws.LastChangedAt,
				ElapsedMs:     time.Since(ws.LastChangedAt).Milliseconds(),
				ThresholdMs:   thresholdMs,
			}, fmt.Errorf("wait_until_idle: timed out after %d ms: %w", timeoutMs, ctx.Err())
		case <-ticker.C:
			content, err := capture.CapturePane(ws.TmuxSession, 200)
			if err != nil {
				return IdleStatus{}, fmt.Errorf("capturing pane for %s: %w", ws.Name, err)
			}
			hash := hashContent(content)
			if hash != ws.LastCaptureHash {
				now := time.Now()
				if err := updater.UpdateIdleState(ws.ID, hash, now); err != nil {
					return IdleStatus{}, fmt.Errorf("updating workspace hash: %w", err)
				}
				ws.LastCaptureHash = hash
				ws.LastChangedAt = now
				continue
			}
			elapsed := time.Since(ws.LastChangedAt).Milliseconds()
			if elapsed >= thresholdMs {
				return IdleStatus{
					Idle:          true,
					LastChangedAt: ws.LastChangedAt,
					ElapsedMs:     elapsed,
					ThresholdMs:   thresholdMs,
				}, nil
			}
		}
	}
}

// trackingUpdater wraps a WorkspaceUpdater and records the most recent hash/time per workspace ID
// so IsIdle can refresh workspace state between the two passes.
type trackingUpdater struct {
	inner     WorkspaceUpdater
	mu        sync.Mutex
	hashes    map[string]string
	changedAt map[string]time.Time
}

func newTrackingUpdater(inner WorkspaceUpdater) *trackingUpdater {
	return &trackingUpdater{
		inner:     inner,
		hashes:    make(map[string]string),
		changedAt: make(map[string]time.Time),
	}
}

func (t *trackingUpdater) UpdateIdleState(id, hash string, changedAt time.Time) error {
	t.mu.Lock()
	t.hashes[id] = hash
	t.changedAt[id] = changedAt
	t.mu.Unlock()
	return t.inner.UpdateIdleState(id, hash, changedAt)
}

// IsIdle runs two concurrent idle-check passes on all workspaces, separated by pollMs.
// pollMs ≤ 0 defaults to 500 ms.
// Returns map[workspaceID]IdleStatus. Per-workspace errors are logged to stderr and that
// workspace is omitted from the result map.
func IsIdle(
	ctx context.Context,
	workspaces []WorkspaceState,
	capture PaneCapture,
	updater WorkspaceUpdater,
	thresholdMs int64,
	pollMs int64,
) map[string]IdleStatus {
	if len(workspaces) == 0 {
		return map[string]IdleStatus{}
	}
	if pollMs <= 0 {
		pollMs = 500
	}

	tracker := newTrackingUpdater(updater)

	type checkResult struct {
		id     string
		status IdleStatus
		err    error
	}

	fanOut := func(wss []WorkspaceState) map[string]IdleStatus {
		ch := make(chan checkResult, len(wss))
		var wg sync.WaitGroup
		for _, ws := range wss {
			wg.Add(1)
			go func(w WorkspaceState) {
				defer wg.Done()
				s, err := Check(ctx, w, capture, tracker, thresholdMs)
				ch <- checkResult{id: w.ID, status: s, err: err}
			}(ws)
		}
		wg.Wait()
		close(ch)
		out := make(map[string]IdleStatus, len(wss))
		for r := range ch {
			if r.err != nil {
				fmt.Fprintf(os.Stderr, "IsIdle: check error for %s: %v\n", r.id, r.err)
				continue
			}
			out[r.id] = r.status
		}
		return out
	}

	// First pass: seed tracker with the current pane hashes.
	fanOut(workspaces)

	// Refresh workspace states using hashes captured by the first pass so the
	// second pass correctly detects stability rather than re-treating a changed
	// hash as a new change.
	refreshed := make([]WorkspaceState, len(workspaces))
	copy(refreshed, workspaces)
	tracker.mu.Lock()
	for i := range refreshed {
		if h, ok := tracker.hashes[refreshed[i].ID]; ok {
			refreshed[i].LastCaptureHash = h
			refreshed[i].LastChangedAt = tracker.changedAt[refreshed[i].ID]
		}
	}
	tracker.mu.Unlock()

	time.Sleep(time.Duration(pollMs) * time.Millisecond)

	// Second pass — return these results.
	return fanOut(refreshed)
}

// hashContent returns a hex SHA-256 of the pane content.
func hashContent(content string) string {
	sum := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x", sum)
}

// looksIdle reports whether the last non-empty line of pane output ends with a prompt suffix.
// Used as a heuristic only — do not rely on this alone.
func looksIdle(content, promptSuffix string) bool {
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return strings.HasSuffix(lines[i], promptSuffix)
		}
	}
	return false
}

// CheckWithPromptHeuristic is like Check but also uses the last-line prompt heuristic as a
// tiebreaker when elapsed is between 80% and 100% of the threshold.
func CheckWithPromptHeuristic(
	ctx context.Context,
	ws WorkspaceState,
	capture PaneCapture,
	updater WorkspaceUpdater,
	thresholdMs int64,
	promptSuffix string,
) (IdleStatus, error) {
	content, err := capture.CapturePane(ws.TmuxSession, 200)
	if err != nil {
		return IdleStatus{}, fmt.Errorf("capturing pane for %s: %w", ws.Name, err)
	}

	hash := hashContent(content)

	if hash != ws.LastCaptureHash {
		now := time.Now()
		if err := updater.UpdateIdleState(ws.ID, hash, now); err != nil {
			return IdleStatus{}, fmt.Errorf("updating workspace hash: %w", err)
		}
		return IdleStatus{Idle: false, LastChangedAt: now, ElapsedMs: 0, ThresholdMs: thresholdMs}, nil
	}

	elapsed := time.Since(ws.LastChangedAt).Milliseconds()
	isIdle := elapsed >= thresholdMs

	// Tiebreaker: if within 80–100% of threshold, use prompt heuristic.
	if !isIdle && elapsed >= (thresholdMs*80/100) {
		isIdle = looksIdle(content, promptSuffix)
	}

	return IdleStatus{
		Idle:          isIdle,
		LastChangedAt: ws.LastChangedAt,
		ElapsedMs:     elapsed,
		ThresholdMs:   thresholdMs,
	}, nil
}
