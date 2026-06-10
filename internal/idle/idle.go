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

// WaitUntilIdleMulti polls multiple workspaces until the idle condition is satisfied.
// mode "all" (default): returns when every workspace is idle.
// mode "any": returns as soon as at least one workspace is idle.
// timeoutMs ≤ 0 defaults to 600 000 ms. Poll interval is hardcoded at 500 ms.
// Returns a map of workspace ID → idle flag and a timedOut boolean.
func WaitUntilIdleMulti(
	ctx context.Context,
	workspaces []WorkspaceState,
	capture PaneCapture,
	updater WorkspaceUpdater,
	thresholdMs, timeoutMs int64,
	mode string,
) (map[string]bool, bool) {
	if timeoutMs <= 0 {
		timeoutMs = 600_000
	}
	if mode == "" {
		mode = "all"
	}

	deadline := time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	states := make([]WorkspaceState, len(workspaces))
	copy(states, workspaces)
	doneIDs := make(map[string]bool, len(workspaces))

	buildResult := func() map[string]bool {
		out := make(map[string]bool, len(workspaces))
		for _, ws := range workspaces {
			out[ws.ID] = doneIDs[ws.ID]
		}
		return out
	}

	satisfied := func() bool {
		if mode == "any" {
			for _, d := range doneIDs {
				if d {
					return true
				}
			}
			return false
		}
		// "all"
		for i := range states {
			if !doneIDs[states[i].ID] {
				return false
			}
		}
		return true
	}

	runCheck := func() {
		type paneResult struct {
			stateIdx int
			hash     string
			changed  time.Time
			isIdle   bool
			err      error
		}

		toCheck := make([]int, 0, len(states))
		for i := range states {
			if !doneIDs[states[i].ID] {
				toCheck = append(toCheck, i)
			}
		}
		if len(toCheck) == 0 {
			return
		}

		ch := make(chan paneResult, len(toCheck))
		var wg sync.WaitGroup
		for _, idx := range toCheck {
			wg.Add(1)
			go func(i int, ws WorkspaceState) {
				defer wg.Done()
				content, err := capture.CapturePane(ws.TmuxSession, 200)
				if err != nil {
					ch <- paneResult{stateIdx: i, err: err}
					return
				}
				hash := hashContent(content)
				if hash != ws.LastCaptureHash {
					now := time.Now()
					if upErr := updater.UpdateIdleState(ws.ID, hash, now); upErr != nil {
						fmt.Fprintf(os.Stderr, "WaitUntilIdleMulti: update error for %s: %v\n", ws.ID, upErr)
					}
					ch <- paneResult{stateIdx: i, hash: hash, changed: now}
					return
				}
				elapsed := time.Since(ws.LastChangedAt).Milliseconds()
				ch <- paneResult{stateIdx: i, hash: ws.LastCaptureHash, changed: ws.LastChangedAt, isIdle: elapsed >= thresholdMs}
			}(idx, states[idx])
		}
		wg.Wait()
		close(ch)

		for r := range ch {
			if r.err != nil {
				fmt.Fprintf(os.Stderr, "WaitUntilIdleMulti: capture error: %v\n", r.err)
				continue
			}
			states[r.stateIdx].LastCaptureHash = r.hash
			states[r.stateIdx].LastChangedAt = r.changed
			if r.isIdle {
				doneIDs[states[r.stateIdx].ID] = true
			}
		}
	}

	// Immediate first check so callers don't wait a full poll interval when already idle.
	runCheck()
	if satisfied() {
		return buildResult(), false
	}

	const pollMs int64 = 500
	ticker := time.NewTicker(time.Duration(pollMs) * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return buildResult(), true
		case <-ticker.C:
			runCheck()
			if satisfied() {
				return buildResult(), false
			}
		}
	}
}

// IsIdle polls each workspace pane concurrently for thresholdMs, sampling every pollMs.
// If the pane hash is stable for the entire window the workspace is idle; any change or
// context cancellation marks it not idle.
// pollMs ≤ 0 defaults to 100 ms; thresholdMs ≤ 0 defaults to 1000 ms.
// Per-workspace capture errors are logged to stderr and that workspace is omitted from the result.
func IsIdle(
	ctx context.Context,
	workspaces []WorkspaceState,
	capture PaneCapture,
	thresholdMs int64,
	pollMs int64,
) map[string]IdleStatus {
	if len(workspaces) == 0 {
		return map[string]IdleStatus{}
	}
	if pollMs <= 0 {
		pollMs = 100
	}
	if thresholdMs <= 0 {
		thresholdMs = 1000
	}

	type result struct {
		id     string
		status IdleStatus
	}

	ch := make(chan result, len(workspaces))
	var wg sync.WaitGroup

	for _, ws := range workspaces {
		wg.Add(1)
		go func(w WorkspaceState) {
			defer wg.Done()

			baseline, err := capture.CapturePane(w.TmuxSession, 200)
			if err != nil {
				fmt.Fprintf(os.Stderr, "IsIdle: capture error for %s: %v\n", w.ID, err)
				return
			}
			baselineHash := hashContent(baseline)

			deadline := time.Now().Add(time.Duration(thresholdMs) * time.Millisecond)
			ticker := time.NewTicker(time.Duration(pollMs) * time.Millisecond)
			defer ticker.Stop()

			for {
				select {
				case <-ctx.Done():
					ch <- result{id: w.ID, status: IdleStatus{Idle: false, ThresholdMs: thresholdMs}}
					return
				case t := <-ticker.C:
					content, err := capture.CapturePane(w.TmuxSession, 200)
					if err != nil {
						fmt.Fprintf(os.Stderr, "IsIdle: capture error for %s: %v\n", w.ID, err)
						return
					}
					hash := hashContent(content)
					if hash != baselineHash {
						ch <- result{id: w.ID, status: IdleStatus{Idle: false, ThresholdMs: thresholdMs}}
						return
					}
					if !t.Before(deadline) {
						ch <- result{id: w.ID, status: IdleStatus{Idle: true, ThresholdMs: thresholdMs}}
						return
					}
				}
			}
		}(ws)
	}

	wg.Wait()
	close(ch)

	out := make(map[string]IdleStatus, len(workspaces))
	for r := range ch {
		out[r.id] = r.status
	}
	return out
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
