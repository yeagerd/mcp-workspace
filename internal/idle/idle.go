// Package idle detects whether a Claude Code session is busy or waiting for input.
// Detection is based on hashing pane output: a stable hash over the threshold duration means idle.
package idle

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"github.com/articulant/tmux-harness/internal/store"
)

// IdleStatus is the result of a single idle check.
type IdleStatus struct {
	Idle          bool
	LastChangedAt time.Time
	ElapsedMs     int64
	ThresholdMs   int64
}

// PaneCapture abstracts the tmux capture-pane call so tests can inject fakes.
type PaneCapture interface {
	CapturePane(sessionName string, lines int) (string, error)
}

// WorkspaceUpdater abstracts store.Update so tests can inject fakes.
type WorkspaceUpdater interface {
	Update(id string, apply func(*store.Workspace)) error
}

// Check determines whether the workspace session is idle.
//
//  1. Capture the pane (200 lines).
//  2. SHA-256 hash the content.
//  3. If hash changed → update store, return busy.
//  4. If hash same → compute elapsed; if >= threshold, return idle.
func Check(
	ctx context.Context,
	ws store.Workspace,
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
		if err := updater.Update(ws.ID, func(w *store.Workspace) {
			w.LastCaptureHash = hash
			w.LastChangedAt = now
		}); err != nil {
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
	ws store.Workspace,
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
				if err := updater.Update(ws.ID, func(w *store.Workspace) {
					w.LastCaptureHash = hash
					w.LastChangedAt = now
				}); err != nil {
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
	ws store.Workspace,
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
		if err := updater.Update(ws.ID, func(w *store.Workspace) {
			w.LastCaptureHash = hash
			w.LastChangedAt = now
		}); err != nil {
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
