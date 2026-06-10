package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// connect spawns hangar as a subprocess and returns an initialized MCP client.
// cleanup must be called when done to kill the subprocess and release resources.
func connect(ctx context.Context, opts globalOpts) (*mcpclient.Client, func(), error) {
	binary, err := resolveBinary(opts.binaryPath)
	if err != nil {
		return nil, nil, err
	}

	// --config flag takes precedence over .mcp.json args.
	args := opts.serverArgs
	if opts.configPath != "" {
		args = []string{"--config", opts.configPath}
	}
	if args == nil {
		args = []string{}
	}

	// Strip HANGAR_* env vars so parent-shell config doesn't conflict, then
	// inject env vars sourced from .mcp.json (e.g. HANGAR_REPO_PATH).
	cmdFunc := transport.WithCommandFunc(func(
		fCtx context.Context, command string, env []string, fArgs []string,
	) (*exec.Cmd, error) {
		cmd := exec.CommandContext(fCtx, command, fArgs...)
		filtered := make([]string, 0, len(os.Environ()))
		for _, e := range os.Environ() {
			if !strings.HasPrefix(e, "HANGAR_") {
				filtered = append(filtered, e)
			}
		}
		cmd.Env = append(filtered, env...)
		for k, v := range opts.serverEnv {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		return cmd, nil
	})

	c, err := mcpclient.NewStdioMCPClientWithOptions(binary, nil, args, cmdFunc)
	if err != nil {
		return nil, nil, fmt.Errorf("starting hangar: %w", err)
	}

	// Forward subprocess stderr to our stderr.
	if r, ok := mcpclient.GetStderr(c); ok {
		go func() {
			_, _ = io.Copy(os.Stderr, r)
		}()
	}

	cleanup := func() {
		_ = c.Close()
	}

	_, err = c.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo: mcp.Implementation{
				Name:    "harness-client",
				Version: clientVersion,
			},
		},
	})
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("initializing MCP client: %w", err)
	}

	return c, cleanup, nil
}

// resolveBinary finds the hangar binary. Prefers the given path, then
// bin/hangar relative to the running executable, then $PATH.
func resolveBinary(path string) (string, error) {
	if path != "" {
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("binary not found: %s", path)
		}
		return path, nil
	}

	// Try bin/hangar relative to the running executable.
	exe, err := os.Executable()
	if err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "hangar")
		if runtime.GOOS == "windows" {
			candidate += ".exe"
		}
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}

	// Fall back to $PATH.
	found, err := exec.LookPath("hangar")
	if err != nil {
		return "", fmt.Errorf("hangar binary not found in PATH or next to harness-client; use --binary to specify")
	}
	return found, nil
}
