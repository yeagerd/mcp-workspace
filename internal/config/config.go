package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Repo holds the configuration for a single git repository.
type Repo struct {
	Path         string `json:"path"`
	WorktreeRoot string `json:"worktreeRoot"` // optional; defaults to ../worktrees
}

// Config holds all runtime configuration for tmux-harness.
type Config struct {
	// Deprecated: use Repos
	RepoPath string `json:"repoPath"`
	// Deprecated: use Repos
	WorktreeRoot string `json:"worktreeRoot"`

	Repos           map[string]Repo `json:"repos"`
	StorePath       string          `json:"storePath"`
	ClaudeCmd       string          `json:"claudeCmd"`
	IdleThresholdMs int             `json:"idleThresholdMs"`
	SessionPrefix   string          `json:"sessionPrefix"`
	MaxWorkspaces   int             `json:"maxWorkspaces"`
}

const (
	defaultClaudeCmd       = "claude"
	defaultIdleThresholdMs = 5000
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
	if cfg.StorePath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		cfg.StorePath = filepath.Join(home, ".config", "tmux-harness", "workspaces.json")
	}

	// Derive WorktreeRoot for each repos entry that omitted it.
	for alias, repo := range cfg.Repos {
		if repo.WorktreeRoot == "" && repo.Path != "" {
			repo.WorktreeRoot = filepath.Join(filepath.Dir(repo.Path), "worktrees")
			cfg.Repos[alias] = repo
		}
	}

	// Environment variable overrides.
	applyEnvString("HARNESS_REPO_PATH", &cfg.RepoPath)
	applyEnvString("HARNESS_WORKTREE_ROOT", &cfg.WorktreeRoot)
	applyEnvString("HARNESS_STORE_PATH", &cfg.StorePath)
	applyEnvString("HARNESS_CLAUDE_CMD", &cfg.ClaudeCmd)
	applyEnvInt("HARNESS_IDLE_THRESHOLD_MS", &cfg.IdleThresholdMs)
	applyEnvString("HARNESS_SESSION_PREFIX", &cfg.SessionPrefix)
	applyEnvInt("HARNESS_MAX_WORKSPACES", &cfg.MaxWorkspaces)

	// Re-apply WorktreeRoot default if RepoPath was set by env and WorktreeRoot still empty.
	if cfg.WorktreeRoot == "" && cfg.RepoPath != "" {
		cfg.WorktreeRoot = filepath.Join(filepath.Dir(cfg.RepoPath), "worktrees")
	}

	// Parse HARNESS_REPOS: comma-separated alias=path[:worktreeRoot] pairs.
	if v := os.Getenv("HARNESS_REPOS"); v != "" {
		if cfg.Repos == nil {
			cfg.Repos = make(map[string]Repo)
		}
		for _, entry := range strings.Split(v, ",") {
			entry = strings.TrimSpace(entry)
			if entry == "" {
				continue
			}
			alias, pathPart, found := strings.Cut(entry, "=")
			if !found || alias == "" {
				return nil, fmt.Errorf("HARNESS_REPOS: invalid entry %q (expected alias=path or alias=path:worktreeRoot)", entry)
			}
			var repo Repo
			if path, wtr, hasWtr := strings.Cut(pathPart, ":"); hasWtr {
				repo.Path = path
				repo.WorktreeRoot = wtr
			} else {
				repo.Path = pathPart
				repo.WorktreeRoot = filepath.Join(filepath.Dir(pathPart), "worktrees")
			}
			cfg.Repos[alias] = repo
		}
	}

	return cfg, nil
}

// Validate checks that cfg is internally consistent and the filesystem prerequisites exist.
func Validate(cfg *Config) error {
	// Ambiguous config: both multi-repo map and legacy single-repo field are set.
	if len(cfg.Repos) > 0 && cfg.RepoPath != "" {
		return fmt.Errorf("repos and repoPath cannot both be set; use repos only")
	}

	// Backward compat shim: synthesise a "default" entry from legacy RepoPath/WorktreeRoot.
	if len(cfg.Repos) == 0 && cfg.RepoPath != "" {
		if cfg.Repos == nil {
			cfg.Repos = make(map[string]Repo)
		}
		cfg.Repos["default"] = Repo{
			Path:         cfg.RepoPath,
			WorktreeRoot: cfg.WorktreeRoot,
		}
	}

	if len(cfg.Repos) == 0 {
		return fmt.Errorf("at least one repo must be configured (set repos in config or repoPath for single-repo use)")
	}

	// Validate each repo entry.
	for alias, repo := range cfg.Repos {
		if repo.Path == "" {
			return fmt.Errorf("repo %q: path is required", alias)
		}
		if _, err := os.Stat(repo.Path); os.IsNotExist(err) {
			return fmt.Errorf("repo %q: path %q does not exist", alias, repo.Path)
		}
		gitPath := filepath.Join(repo.Path, ".git")
		if _, err := os.Stat(gitPath); os.IsNotExist(err) {
			return fmt.Errorf("repo %q: path %q does not contain a .git directory or file", alias, repo.Path)
		}
		if repo.WorktreeRoot == "" {
			return fmt.Errorf("repo %q: worktreeRoot is required", alias)
		}
		parentDir := filepath.Dir(repo.WorktreeRoot)
		if _, err := os.Stat(parentDir); os.IsNotExist(err) {
			return fmt.Errorf("repo %q: worktreeRoot parent directory %q does not exist", alias, parentDir)
		}
		if err := os.MkdirAll(repo.WorktreeRoot, 0o755); err != nil {
			return fmt.Errorf("repo %q: creating worktreeRoot %q: %w", alias, repo.WorktreeRoot, err)
		}
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
	fmt.Fprintln(os.Stderr, "tmux-harness config:")
	for alias, repo := range cfg.Repos {
		fmt.Fprintf(os.Stderr, "  repo[%s]: path=%s worktreeRoot=%s\n", alias, repo.Path, repo.WorktreeRoot)
	}
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
