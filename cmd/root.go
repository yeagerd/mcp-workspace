package cmd

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/articulant/tmux-harness/internal/config"
	"github.com/articulant/tmux-harness/internal/store"
	"github.com/articulant/tmux-harness/internal/tmux"
	"github.com/articulant/tmux-harness/internal/tools"
	"github.com/articulant/tmux-harness/internal/workspace"
	"github.com/articulant/tmux-harness/internal/worktree"
	"github.com/mark3labs/mcp-go/server"
)

const version = "0.1.0"

func Execute() error {
	var configPath string
	var showVersion bool
	flag.StringVar(&configPath, "config", "", "path to config JSON file")
	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	flag.Parse()

	if showVersion {
		fmt.Fprintf(os.Stderr, "tmux-harness %s\n", version)
		return nil
	}

	return run(configPath)
}

func run(configPath string) error {
	// Step 1: Load and validate config.
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	if err := config.Validate(cfg); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	config.PrintSummary(cfg)

	// Step 2: Initialize store. If the file is corrupt, log and exit.
	s, err := store.NewStore(cfg.StorePath)
	if err != nil {
		return fmt.Errorf("initializing store: %w", err)
	}

	// Step 3: Initialize tmux and worktree clients.
	tmuxClient := tmux.New()
	wtClient := worktree.New(cfg.RepoPath)

	// Step 4: Initialize workspace manager.
	mgr := workspace.New(tmuxClient, wtClient, s, cfg)

	// Step 5: Reconcile — detect orphaned workspaces. Log results to stderr.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Reconcile(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "reconcile warning: %v\n", err)
	}

	// Step 6: Build MCP server and register all tools.
	mcpServer := server.NewMCPServer(
		"tmux-harness",
		version,
		server.WithToolCapabilities(true),
		server.WithResourceCapabilities(true, false),
		server.WithRecovery(),
	)
	tools.Register(mcpServer, mgr, tmuxClient, s, int64(cfg.IdleThresholdMs))

	// Graceful shutdown: on SIGINT/SIGTERM, give in-flight operations up to 5 s
	// then hard-exit. Active tmux sessions and worktrees are intentionally left intact.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		fmt.Fprintf(os.Stderr, "tmux-harness: received %s; waiting up to 5 s for in-flight calls...\n", sig)
		cancel()
		time.Sleep(5 * time.Second)
		fmt.Fprintln(os.Stderr, "tmux-harness: exiting")
		os.Exit(0)
	}()

	// Step 7: Start MCP server — stdout is the MCP transport from this point forward.
	fmt.Fprintln(os.Stderr, "tmux-harness: starting MCP server over stdio")
	return server.ServeStdio(mcpServer)
}
