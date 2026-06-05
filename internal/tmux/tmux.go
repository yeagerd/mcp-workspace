// Package tmux wraps the tmux CLI. No other package in this project shells out to tmux.
// SAFETY: Every exec.Command call must pass the binary and args as separate strings.
// Never construct a shell command string — that invites injection.
package tmux

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ErrSessionNotFound is returned when a named tmux session does not exist.
var ErrSessionNotFound = errors.New("tmux session not found")

// Executor abstracts shell invocations so unit tests can inject fakes.
type Executor interface {
	Run(name string, args ...string) ([]byte, error)
}

// systemExecutor calls the real binary.
type systemExecutor struct{}

func (s *systemExecutor) Run(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).Output() //nolint:gosec // args are passed as separate values
}

// Client is a thin wrapper around the tmux CLI.
type Client struct {
	exec Executor
}

// New returns a Client that shells out to the real tmux binary.
func New() *Client {
	return &Client{exec: &systemExecutor{}}
}

// NewWithExecutor returns a Client using a custom Executor (for testing).
func NewWithExecutor(e Executor) *Client {
	return &Client{exec: e}
}

// isSessionNotFound reports whether an error from tmux indicates a missing session.
func isSessionNotFound(output []byte, err error) bool {
	if err == nil {
		return false
	}
	combined := string(output) + err.Error()
	return strings.Contains(combined, "can't find session") ||
		strings.Contains(combined, "session not found") ||
		strings.Contains(combined, "no server running") ||
		strings.Contains(combined, "error connecting to server")
}

// SessionExists reports whether a session named prefix+name exists.
func (c *Client) SessionExists(prefix, name string) (bool, error) {
	sessions, err := c.ListSessions(prefix)
	if err != nil {
		return false, err
	}
	target := prefix + name
	for _, s := range sessions {
		if s == target {
			return true, nil
		}
	}
	return false, nil
}

// NewSession creates a new detached tmux session named sessionName with startDir as the cwd.
func (c *Client) NewSession(sessionName, startDir string) error {
	out, err := c.exec.Run("tmux", "new-session", "-d", "-s", sessionName, "-c", startDir)
	if err != nil {
		return fmt.Errorf("tmux new-session %q: %w (output: %s)", sessionName, err, out)
	}
	return nil
}

// KillSession kills the named session. Returns nil if the session does not exist.
func (c *Client) KillSession(sessionName string) error {
	out, err := c.exec.Run("tmux", "kill-session", "-t", sessionName)
	if err != nil {
		if isSessionNotFound(out, err) {
			return nil
		}
		return fmt.Errorf("tmux kill-session %q: %w (output: %s)", sessionName, err, out)
	}
	return nil
}

// SendKeys sends text to the named session. If pressEnter is true, an Enter keystroke is appended.
func (c *Client) SendKeys(sessionName, text string, pressEnter bool) error {
	args := []string{"send-keys", "-t", sessionName, text}
	if pressEnter {
		args = append(args, "Enter")
	}
	out, err := c.exec.Run("tmux", args...)
	if err != nil {
		if isSessionNotFound(out, err) {
			return fmt.Errorf("%w: %s", ErrSessionNotFound, sessionName)
		}
		return fmt.Errorf("tmux send-keys %q: %w (output: %s)", sessionName, err, out)
	}
	return nil
}

// CapturePane captures the last lines lines of terminal output from the named session.
func (c *Client) CapturePane(sessionName string, lines int) (string, error) {
	startLine := fmt.Sprintf("-%d", lines)
	out, err := c.exec.Run("tmux", "capture-pane", "-p", "-t", sessionName, "-S", startLine)
	if err != nil {
		if isSessionNotFound(out, err) {
			return "", fmt.Errorf("%w: %s", ErrSessionNotFound, sessionName)
		}
		return "", fmt.Errorf("tmux capture-pane %q: %w (output: %s)", sessionName, err, out)
	}
	return string(out), nil
}

// ListSessions returns all session names that start with prefix.
// If the tmux server is not running, returns an empty slice (not an error).
func (c *Client) ListSessions(prefix string) ([]string, error) {
	out, err := c.exec.Run("tmux", "list-sessions", "-F", "#{session_name}")
	if err != nil {
		// No server running is not an error for list operations.
		combined := string(out) + err.Error()
		if strings.Contains(combined, "no server running") ||
			strings.Contains(combined, "error connecting to server") ||
			strings.Contains(combined, "no sessions") {
			return []string{}, nil
		}
		return nil, fmt.Errorf("tmux list-sessions: %w (output: %s)", err, out)
	}
	return parseSessionList(string(out), prefix), nil
}

// RenameSession renames a tmux session from oldName to newName.
func (c *Client) RenameSession(oldName, newName string) error {
	out, err := c.exec.Run("tmux", "rename-session", "-t", oldName, newName)
	if err != nil {
		if isSessionNotFound(out, err) {
			return fmt.Errorf("%w: %s", ErrSessionNotFound, oldName)
		}
		return fmt.Errorf("tmux rename-session %q -> %q: %w (output: %s)", oldName, newName, err, out)
	}
	return nil
}

// parseSessionList parses the output of `tmux list-sessions -F #{session_name}` and
// returns only names that start with prefix. Pure function — no exec dependency.
func parseSessionList(output, prefix string) []string {
	var result []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, prefix) {
			result = append(result, line)
		}
	}
	return result
}
