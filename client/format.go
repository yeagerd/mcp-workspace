package main

import (
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// workspaceSummary mirrors the JSON shape returned by workspace_* tools.
type workspaceSummary struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Status       string    `json:"status"`
	Branch       string    `json:"branch"`
	TmuxSession  string    `json:"tmuxSession"`
	CreatedAt    time.Time `json:"createdAt"`
	WorktreePath string    `json:"worktreePath"`
	RepoAlias    string    `json:"repoAlias,omitempty"`
}

// errWriter accumulates the first write error and short-circuits subsequent writes.
type errWriter struct {
	w   io.Writer
	err error
}

func (ew *errWriter) printf(format string, args ...any) {
	if ew.err != nil {
		return
	}
	_, ew.err = fmt.Fprintf(ew.w, format, args...)
}

const (
	colIDWidth     = 8
	colNameWidth   = 16
	colStatusWidth = 8
	colBranchWidth = 16
	colRepoWidth   = 6
)

// truncate shortens s to maxLen, appending "…" if truncated.
func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-1]) + "…"
}

// printTable renders a fixed-width table of workspaces.
func printTable(ws []workspaceSummary, w io.Writer) {
	ew := &errWriter{w: w}
	ew.printf("%-*s  %-*s  %-*s  %-*s  %-*s  %s\n",
		colIDWidth, "ID",
		colNameWidth, "NAME",
		colStatusWidth, "STATUS",
		colBranchWidth, "BRANCH",
		colRepoWidth, "REPO",
		"CREATED",
	)
	for _, s := range ws {
		repo := s.RepoAlias
		if repo == "" {
			repo = "-"
		}
		created := "-"
		if !s.CreatedAt.IsZero() {
			created = s.CreatedAt.Local().Format("2006-01-02 15:04")
		}
		id := s.ID
		if len(id) > colIDWidth {
			id = id[:colIDWidth]
		}
		ew.printf("%-*s  %-*s  %-*s  %-*s  %-*s  %s\n",
			colIDWidth, id,
			colNameWidth, truncate(s.Name, colNameWidth),
			colStatusWidth, truncate(s.Status, colStatusWidth),
			colBranchWidth, truncate(s.Branch, colBranchWidth),
			colRepoWidth, truncate(repo, colRepoWidth),
			created,
		)
	}
}

// printWorkspace prints a single workspace as key-value pairs.
func printWorkspace(ws workspaceSummary, w io.Writer) {
	ew := &errWriter{w: w}
	ew.printf("id:       %s\n", ws.ID)
	ew.printf("name:     %s\n", ws.Name)
	ew.printf("status:   %s\n", ws.Status)
	ew.printf("branch:   %s\n", ws.Branch)
	ew.printf("session:  %s\n", ws.TmuxSession)
	ew.printf("worktree: %s\n", ws.WorktreePath)
	if !ws.CreatedAt.IsZero() {
		ew.printf("created:  %s\n", ws.CreatedAt.Local().Format("2006-01-02 15:04:05"))
	}
}

// prettyPrint writes indented JSON to stdout.
func prettyPrint(raw json.RawMessage) error {
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	fmt.Printf("%s\n", out)
	return nil
}
