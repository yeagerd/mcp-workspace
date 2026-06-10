package config

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Config holds all runtime configuration for hangar.
type Config struct {
	RepoPath        string `json:"repoPath"`
	WorktreeRoot    string `json:"worktreeRoot"`
	StorePath       string `json:"storePath"`
	ClaudeCmd       string `json:"claudeCmd"`
	IdleThresholdMs int    `json:"idleThresholdMs"`
	SessionPrefix   string `json:"sessionPrefix"`
	MaxWorkspaces   int    `json:"maxWorkspaces"`
}

const (
	defaultClaudeCmd       = "claude"
	defaultIdleThresholdMs = 1000
	defaultSessionPrefix   = "harness-"
	defaultMaxWorkspaces   = 10
)

// Load reads config from configPath (if non-empty and present), then overrides
// with environment variables. Env vars always take priority over file values.
func Load(configPath string) (*Config, error) {
	cfg := &Config{
		ClaudeCmd:       defaultClaudeCmd,
		IdleThresholdMs: defaultIdleThresholdMs,
		SessionPrefix:   defaultSessionPrefix,
		MaxWorkspaces:   defaultMaxWorkspaces,
	}

	if configPath != "" {
		data, err := os.ReadFile(configPath)
		if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("reading config file: %w", err)
		}
		if err == nil {
			if err := json.Unmarshal(data, cfg); err != nil {
				return nil, fmt.Errorf("parsing config file: %w", err)
			}
		}
	}

	// Set defaults that depend on RepoPath after file load.
	if cfg.WorktreeRoot == "" && cfg.RepoPath != "" {
		cfg.WorktreeRoot = filepath.Join(filepath.Dir(cfg.RepoPath), "worktrees")
	}

	// Environment variable overrides.
	applyEnvString("HARNESS_REPO_PATH", &cfg.RepoPath)
	applyEnvString("HARNESS_WORKTREE_ROOT", &cfg.WorktreeRoot)
	applyEnvString("HARNESS_STORE_PATH", &cfg.StorePath)
	applyEnvString("HARNESS_CLAUDE_CMD", &cfg.ClaudeCmd)
	applyEnvInt("HARNESS_IDLE_THRESHOLD_MS", &cfg.IdleThresholdMs)
	applyEnvString("HARNESS_SESSION_PREFIX", &cfg.SessionPrefix)
	applyEnvInt("HARNESS_MAX_WORKSPACES", &cfg.MaxWorkspaces)

	// Auto-detect repoPath from git if not set by config or env.
	if cfg.RepoPath == "" {
		if detected, err := detectRepoPathFn(); err == nil {
			cfg.RepoPath = detected
		}
	}

	// Re-apply defaults that depend on RepoPath after env vars and auto-detection.
	if cfg.WorktreeRoot == "" && cfg.RepoPath != "" {
		cfg.WorktreeRoot = filepath.Join(filepath.Dir(cfg.RepoPath), "worktrees")
	}
	if cfg.StorePath == "" {
		if cfg.RepoPath != "" {
			cfg.StorePath = filepath.Join(cfg.RepoPath, ".hangar", "workspaces.json")
		} else {
			home, err := os.UserHomeDir()
			if err != nil {
				home = "."
			}
			cfg.StorePath = filepath.Join(home, ".config", "hangar", "workspaces.json")
		}
	}

	return cfg, nil
}

// Validate checks that cfg is internally consistent and the filesystem prerequisites exist.
func Validate(cfg *Config) error {
	if cfg.RepoPath == "" {
		return fmt.Errorf("repoPath is not set and could not be auto-detected; set HARNESS_REPO_PATH or run from within a git repository")
	}
	if _, err := os.Stat(cfg.RepoPath); os.IsNotExist(err) {
		return fmt.Errorf("repoPath %q does not exist", cfg.RepoPath)
	}

	// Check for .git directory or file.
	gitPath := filepath.Join(cfg.RepoPath, ".git")
	if _, err := os.Stat(gitPath); os.IsNotExist(err) {
		return fmt.Errorf("repoPath %q does not contain a .git directory or file", cfg.RepoPath)
	}

	// WorktreeRoot parent must exist; create the directory itself if absent.
	if cfg.WorktreeRoot == "" {
		return fmt.Errorf("worktreeRoot is required")
	}
	parentDir := filepath.Dir(cfg.WorktreeRoot)
	if _, err := os.Stat(parentDir); os.IsNotExist(err) {
		return fmt.Errorf("worktreeRoot parent directory %q does not exist", parentDir)
	}
	if err := os.MkdirAll(cfg.WorktreeRoot, 0o755); err != nil {
		return fmt.Errorf("creating worktreeRoot %q: %w", cfg.WorktreeRoot, err)
	}

	// StorePath parent must exist; create if absent.
	if cfg.StorePath == "" {
		return fmt.Errorf("storePath is required")
	}
	storeDir := filepath.Dir(cfg.StorePath)
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		return fmt.Errorf("creating storePath directory %q: %w", storeDir, err)
	}

	if cfg.MaxWorkspaces < 1 || cfg.MaxWorkspaces > 50 {
		return fmt.Errorf("maxWorkspaces must be between 1 and 50, got %d", cfg.MaxWorkspaces)
	}

	return nil
}

// PrintSummary logs resolved config values to stderr, marking defaults.
func PrintSummary(cfg *Config) {
	fmt.Fprintln(os.Stderr, "hangar config:")
	printField("  repoPath", cfg.RepoPath, "")
	printField("  worktreeRoot", cfg.WorktreeRoot, "")
	printField("  storePath", cfg.StorePath, "")
	printField("  claudeCmd", cfg.ClaudeCmd, defaultClaudeCmd)
	printField("  idleThresholdMs", strconv.Itoa(cfg.IdleThresholdMs), strconv.Itoa(defaultIdleThresholdMs))
	printField("  sessionPrefix", cfg.SessionPrefix, defaultSessionPrefix)
	printField("  maxWorkspaces", strconv.Itoa(cfg.MaxWorkspaces), strconv.Itoa(defaultMaxWorkspaces))
}

func printField(name, value, defaultVal string) {
	if defaultVal != "" && value == defaultVal {
		fmt.Fprintf(os.Stderr, "%s: %s (default)\n", name, value)
	} else {
		fmt.Fprintf(os.Stderr, "%s: %s\n", name, value)
	}
}

func applyEnvString(key string, dst *string) {
	if v := os.Getenv(key); v != "" {
		*dst = v
	}
}

func applyEnvInt(key string, dst *int) {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			*dst = n
		}
	}
}

// detectRepoPathFn is a package-level variable so tests can substitute a fake.
var detectRepoPathFn = detectRepoPath

func detectRepoPath() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
