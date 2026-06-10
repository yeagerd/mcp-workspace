package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_Defaults(t *testing.T) {
	cfg, err := Load("")
	require.NoError(t, err)
	assert.Equal(t, defaultClaudeCmd, cfg.ClaudeCmd)
	assert.Equal(t, defaultIdleThresholdMs, cfg.IdleThresholdMs)
	assert.Equal(t, defaultSessionPrefix, cfg.SessionPrefix)
	assert.Equal(t, defaultMaxWorkspaces, cfg.MaxWorkspaces)
}

func TestLoad_MissingFile(t *testing.T) {
	cfg, err := Load("/nonexistent/path/config.json")
	require.NoError(t, err)
	assert.Equal(t, defaultClaudeCmd, cfg.ClaudeCmd)
}

func TestLoad_FromFile(t *testing.T) {
	// Clear env so file values aren't overridden by the shell environment.
	t.Setenv("HARNESS_REPO_PATH", "")

	tmp := t.TempDir()
	cfgData := map[string]interface{}{
		"repoPath":        "/some/repo",
		"claudeCmd":       "myclaude",
		"maxWorkspaces":   5,
		"sessionPrefix":   "test-",
		"idleThresholdMs": 3000,
	}
	data, err := json.Marshal(cfgData)
	require.NoError(t, err)
	cfgFile := filepath.Join(tmp, "config.json")
	require.NoError(t, os.WriteFile(cfgFile, data, 0o600))

	// Stub auto-detection so the non-existent "/some/repo" isn't replaced.
	orig := detectRepoPathFn
	detectRepoPathFn = func() (string, error) { return "", errors.New("stubbed") }
	t.Cleanup(func() { detectRepoPathFn = orig })

	cfg, err := Load(cfgFile)
	require.NoError(t, err)
	assert.Equal(t, "/some/repo", cfg.RepoPath)
	assert.Equal(t, "myclaude", cfg.ClaudeCmd)
	assert.Equal(t, 5, cfg.MaxWorkspaces)
	assert.Equal(t, "test-", cfg.SessionPrefix)
	assert.Equal(t, 3000, cfg.IdleThresholdMs)
}

func TestLoad_EnvOverrides(t *testing.T) {
	t.Setenv("HARNESS_REPO_PATH", "/env/repo")
	t.Setenv("HARNESS_CLAUDE_CMD", "env-claude")
	t.Setenv("HARNESS_MAX_WORKSPACES", "7")
	t.Setenv("HARNESS_SESSION_PREFIX", "env-")
	t.Setenv("HARNESS_IDLE_THRESHOLD_MS", "9000")

	cfg, err := Load("")
	require.NoError(t, err)
	assert.Equal(t, "/env/repo", cfg.RepoPath)
	assert.Equal(t, "env-claude", cfg.ClaudeCmd)
	assert.Equal(t, 7, cfg.MaxWorkspaces)
	assert.Equal(t, "env-", cfg.SessionPrefix)
	assert.Equal(t, 9000, cfg.IdleThresholdMs)
}

func TestLoad_EnvOverridesFile(t *testing.T) {
	tmp := t.TempDir()
	cfgData := map[string]interface{}{"claudeCmd": "file-claude"}
	data, _ := json.Marshal(cfgData)
	cfgFile := filepath.Join(tmp, "config.json")
	require.NoError(t, os.WriteFile(cfgFile, data, 0o600))

	t.Setenv("HARNESS_CLAUDE_CMD", "env-claude")

	cfg, err := Load(cfgFile)
	require.NoError(t, err)
	assert.Equal(t, "env-claude", cfg.ClaudeCmd)
}

func TestValidate_Valid(t *testing.T) {
	tmp := t.TempDir()
	repoPath := filepath.Join(tmp, "repo")
	require.NoError(t, os.MkdirAll(filepath.Join(repoPath, ".git"), 0o755))

	cfg := &Config{
		RepoPath:      repoPath,
		WorktreeRoot:  filepath.Join(tmp, "worktrees"),
		StorePath:     filepath.Join(tmp, "store", "ws.json"),
		ClaudeCmd:     "claude",
		MaxWorkspaces: 10,
		SessionPrefix: "harness-",
	}
	require.NoError(t, Validate(cfg))
	_, err := os.Stat(cfg.WorktreeRoot)
	assert.NoError(t, err, "worktreeRoot should be created")
}

func TestValidate_InvalidRepoPath(t *testing.T) {
	cfg := &Config{
		RepoPath:      "/nonexistent/repo",
		WorktreeRoot:  "/tmp/wt",
		StorePath:     "/tmp/store/ws.json",
		MaxWorkspaces: 10,
	}
	err := Validate(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not exist")
}

func TestValidate_MissingGit(t *testing.T) {
	tmp := t.TempDir()
	repoPath := filepath.Join(tmp, "repo")
	require.NoError(t, os.MkdirAll(repoPath, 0o755))

	cfg := &Config{
		RepoPath:      repoPath,
		WorktreeRoot:  filepath.Join(tmp, "wt"),
		StorePath:     filepath.Join(tmp, "store", "ws.json"),
		MaxWorkspaces: 10,
	}
	err := Validate(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), ".git")
}

func TestValidate_MaxWorkspacesBounds(t *testing.T) {
	tmp := t.TempDir()
	repoPath := filepath.Join(tmp, "repo")
	require.NoError(t, os.MkdirAll(filepath.Join(repoPath, ".git"), 0o755))

	base := &Config{
		RepoPath:      repoPath,
		WorktreeRoot:  filepath.Join(tmp, "wt"),
		StorePath:     filepath.Join(tmp, "store", "ws.json"),
		SessionPrefix: "h-",
	}

	for _, bad := range []int{0, 101} {
		cfg := *base
		cfg.MaxWorkspaces = bad
		assert.Error(t, Validate(&cfg))
	}
	for _, good := range []int{1, 50, 100} {
		cfg := *base
		cfg.MaxWorkspaces = good
		assert.NoError(t, Validate(&cfg))
	}
}

func TestValidate_EmptyRepoPath(t *testing.T) {
	cfg := &Config{MaxWorkspaces: 10}
	err := Validate(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repoPath is not set")
}

func TestLoad_AutoDetectRepoPath(t *testing.T) {
	// Unset override so auto-detection runs.
	t.Setenv("HARNESS_REPO_PATH", "")

	cfg, err := Load("")
	require.NoError(t, err)
	// When run from within the repo, git detects a non-empty path.
	assert.NotEmpty(t, cfg.RepoPath)
	// StorePath should be local to the detected repo.
	assert.Contains(t, cfg.StorePath, ".hangar")
}

func TestLoad_AutoDetectFails_RepoPathEmpty(t *testing.T) {
	t.Setenv("HARNESS_REPO_PATH", "")

	orig := detectRepoPathFn
	detectRepoPathFn = func() (string, error) {
		return "", errors.New("not a git repository")
	}
	t.Cleanup(func() { detectRepoPathFn = orig })

	cfg, err := Load("")
	require.NoError(t, err)
	assert.Empty(t, cfg.RepoPath)

	err = Validate(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auto-detected")
}
