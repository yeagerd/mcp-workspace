package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConnect_BinaryNotFound(t *testing.T) {
	ctx := context.Background()
	// Use a path that does not exist.
	c, cleanup, err := connect(ctx, globalOpts{binaryPath: "/nonexistent/path/to/tmux-harness"})
	require.Error(t, err)
	assert.Nil(t, c)
	assert.Nil(t, cleanup)
	assert.Contains(t, err.Error(), "binary not found")
}

func TestResolveBinary_ExplicitPath(t *testing.T) {
	// An explicit path that does not exist should return an error.
	_, err := resolveBinary("/does/not/exist")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "binary not found")
}

func TestResolveBinary_EmptyPath_NoExe(t *testing.T) {
	// With no tmux-harness in PATH or next to the test binary, this should
	// either succeed (if tmux-harness happens to be available) or fail with
	// a descriptive error. We only verify no panic occurs.
	_, _ = resolveBinary("")
}
