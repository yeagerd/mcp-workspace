// Package worktree wraps all git worktree operations. No other package calls git directly.
// SAFETY: Every exec.Command call must pass the binary and args as separate strings.
// Never construct a shell command string — that invites injection.
package worktree

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ErrWorktreePathExists is returned when the target path already exists on disk but is
// not registered as a git worktree. The caller can decide whether to force.
var ErrWorktreePathExists = errors.New("worktree path already exists")

// Executor abstracts shell invocations so unit tests can inject fakes.
type Executor interface {
	Run(repoPath, name string, args ...string) ([]byte, error)
}

// systemExecutor calls the real git binary using -C to set the working directory.
type systemExecutor struct{}

func (s *systemExecutor) Run(repoPath, name string, args ...string) ([]byte, error) {
	fullArgs := append([]string{"-C", repoPath}, args...)
	return exec.Command(name, fullArgs...).Output() //nolint:gosec // args are separate values
}

// WorktreeInfo holds parsed metadata for a single git worktree.
type WorktreeInfo struct {
	Path    string
	Branch  string // empty if detached HEAD
	Head    string // commit SHA
	Locked  bool
	Prunable bool
}

// Client manages git worktree operations for a specific repository.
type Client struct {
	repoPath string
	exec     Executor
}

// New returns a Client that operates on repoPath.
func New(repoPath string) *Client {
	return &Client{repoPath: repoPath, exec: &systemExecutor{}}
}

// NewWithExecutor returns a Client using a custom Executor (for testing).
func NewWithExecutor(repoPath string, e Executor) *Client {
	return &Client{repoPath: repoPath, exec: e}
}

// Add creates a new worktree at worktreePath. If createBranch is true, a new branch named
// branchName is created; otherwise the existing branch is checked out.
func (c *Client) Add(worktreePath, branchName string, createBranch bool) error {
	args := []string{"worktree", "add", worktreePath}
	if createBranch {
		args = append(args, "-b", branchName)
	} else {
		args = append(args, branchName)
	}
	out, err := c.exec.Run(c.repoPath, "git", args...)
	if err != nil {
		msg := string(out) + err.Error()
		if strings.Contains(msg, "already exists") {
			return fmt.Errorf("%w: %s", ErrWorktreePathExists, worktreePath)
		}
		return fmt.Errorf("git worktree add %q: %w (output: %s)", worktreePath, err, out)
	}
	return nil
}

// Remove removes the worktree at worktreePath. If force is true, passes --force.
func (c *Client) Remove(worktreePath string, force bool) error {
	args := []string{"worktree", "remove", worktreePath}
	if force {
		args = append(args, "--force")
	}
	out, err := c.exec.Run(c.repoPath, "git", args...)
	if err != nil {
		return fmt.Errorf("git worktree remove %q: %w (output: %s)", worktreePath, err, out)
	}
	return nil
}

// List returns metadata for all registered worktrees.
func (c *Client) List() ([]WorktreeInfo, error) {
	out, err := c.exec.Run(c.repoPath, "git", "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("git worktree list: %w (output: %s)", err, out)
	}
	return parsePorcelain(string(out)), nil
}

// Prune removes stale worktree administrative files.
func (c *Client) Prune() error {
	out, err := c.exec.Run(c.repoPath, "git", "worktree", "prune")
	if err != nil {
		return fmt.Errorf("git worktree prune: %w (output: %s)", err, out)
	}
	return nil
}

// parsePorcelain parses the output of `git worktree list --porcelain` into a slice of
// WorktreeInfo. Pure function with no exec dependency — call it directly from tests.
func parsePorcelain(output string) []WorktreeInfo {
	var result []WorktreeInfo
	var current *WorktreeInfo

	for _, line := range strings.Split(output, "\n") {
		if line == "" {
			if current != nil {
				result = append(result, *current)
				current = nil
			}
			continue
		}

		switch {
		case strings.HasPrefix(line, "worktree "):
			current = &WorktreeInfo{Path: strings.TrimPrefix(line, "worktree ")}
		case current == nil:
			// Skip lines outside a worktree block.
		case strings.HasPrefix(line, "HEAD "):
			current.Head = strings.TrimPrefix(line, "HEAD ")
		case strings.HasPrefix(line, "branch "):
			// branch refs/heads/main → "main"
			ref := strings.TrimPrefix(line, "branch ")
			ref = strings.TrimPrefix(ref, "refs/heads/")
			current.Branch = ref
		case line == "bare":
			// main worktree of a bare repo; leave Branch empty
		case line == "detached":
			// detached HEAD; Branch stays empty
		case strings.HasPrefix(line, "locked"):
			current.Locked = true
		case strings.HasPrefix(line, "prunable"):
			current.Prunable = true
		}
	}

	// Flush a trailing block with no trailing blank line.
	if current != nil {
		result = append(result, *current)
	}

	return result
}
