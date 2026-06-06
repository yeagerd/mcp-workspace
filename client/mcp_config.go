package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type mcpServerEntry struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

// findProjectMCPConfig walks up from dir looking for .mcp.json and returns
// the tmux-harness server entry if found. Returns nil if not found or unparseable.
func findProjectMCPConfig(dir string) *mcpServerEntry {
	for {
		data, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
		if err == nil {
			var cfg struct {
				MCPServers map[string]mcpServerEntry `json:"mcpServers"`
			}
			if err := json.Unmarshal(data, &cfg); err == nil {
				if entry, ok := cfg.MCPServers["tmux-harness"]; ok {
					return &entry
				}
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return nil
}
