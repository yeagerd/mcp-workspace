//go:build integration

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegration_ListEmptyStore(t *testing.T) {
	if os.Getenv("HANGAR_INTEGRATION") != "1" {
		t.Skip("set HANGAR_INTEGRATION=1 to run integration tests")
	}

	// Find repo root (directory containing go.mod).
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	repoRoot := filepath.Dir(filepath.Dir(file))

	tmpDir := t.TempDir()
	harnessBin := filepath.Join(tmpDir, "hangar")
	clientBin := filepath.Join(tmpDir, "harness-client")
	if runtime.GOOS == "windows" {
		harnessBin += ".exe"
		clientBin += ".exe"
	}

	// Build hangar.
	buildCmd := exec.Command("go", "build", "-o", harnessBin, ".")
	buildCmd.Dir = repoRoot
	out, err := buildCmd.CombinedOutput()
	require.NoError(t, err, "building hangar: %s", out)

	// Build harness-client.
	buildCmd = exec.Command("go", "build", "-o", clientBin, "./client")
	buildCmd.Dir = repoRoot
	out, err = buildCmd.CombinedOutput()
	require.NoError(t, err, "building harness-client: %s", out)

	// Write a minimal config JSON with an empty store.
	storePath := filepath.Join(tmpDir, "store.json")
	cfgPath := filepath.Join(tmpDir, "config.json")

	// Create an empty store file (JSON array, not object).
	require.NoError(t, os.WriteFile(storePath, []byte("[]"), 0o600))

	cfg := map[string]any{
		"storePath": storePath,
		"repos": map[string]any{
			"default": map[string]any{
				"path":         repoRoot,
				"worktreeRoot": tmpDir,
			},
		},
	}
	cfgBytes, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cfgPath, cfgBytes, 0o600))

	// Run harness-client list.
	cmd := exec.Command(clientBin, "--binary", harnessBin, "--config", cfgPath, "list")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "harness-client list failed: %s", output)
	assert.Equal(t, 0, cmd.ProcessState.ExitCode())

	// Output should contain the header (ID, NAME, etc.) and no workspace rows.
	outStr := string(output)
	assert.True(t, strings.Contains(outStr, "ID") || outStr == "", "expected header or empty output, got: %q", outStr)
}
