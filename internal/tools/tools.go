// Package tools registers all MCP tool handlers. It is the only package that imports
// the MCP library; all business logic lives in the workspace package.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/articulant/tmux-harness/internal/idle"
	"github.com/articulant/tmux-harness/internal/store"
	"github.com/articulant/tmux-harness/internal/workspace"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Manager is the interface the tool handlers use to access workspace operations.
type Manager interface {
	Create(ctx context.Context, opts workspace.CreateOptions) (store.Workspace, error)
	Archive(ctx context.Context, id string) (store.Workspace, error)
	Delete(ctx context.Context, id string, confirmed bool) error
	List(includeArchived bool) []store.Workspace
	Get(id string) (store.Workspace, error)
	GetByName(name string) (store.Workspace, error)
	SendKeys(id string, text string, pressEnter bool) error
}

// PaneCapture is the interface for capturing tmux pane output.
type PaneCapture interface {
	CapturePane(sessionName string, lines int) (string, error)
}

// StoreUpdater is the interface for the idle checker to update workspace state.
type StoreUpdater interface {
	Update(id string, apply func(*store.Workspace)) error
	Get(id string) (store.Workspace, error)
}

// rateLimiter tracks the last send time per workspace ID.
type rateLimiter struct {
	mu       sync.Mutex
	lastSend map[string]time.Time
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{lastSend: make(map[string]time.Time)}
}

const sendCooldownMs = 200

func (r *rateLimiter) check(id string) (retryAfterMs int64, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if last, exists := r.lastSend[id]; exists {
		elapsed := time.Since(last).Milliseconds()
		if elapsed < sendCooldownMs {
			return sendCooldownMs - elapsed, false
		}
	}
	r.lastSend[id] = time.Now()
	return 0, true
}

// workspaceSummary is the JSON shape for list output.
type workspaceSummary struct {
	ID           string                `json:"id"`
	Name         string                `json:"name"`
	Status       store.WorkspaceStatus `json:"status"`
	Branch       string                `json:"branch"`
	TmuxSession  string                `json:"tmuxSession"`
	CreatedAt    time.Time             `json:"createdAt"`
	WorktreePath string                `json:"worktreePath"`
}

func toSummary(ws store.Workspace) workspaceSummary {
	return workspaceSummary{
		ID:           ws.ID,
		Name:         ws.Name,
		Status:       ws.Status,
		Branch:       ws.Branch,
		TmuxSession:  ws.TmuxSession,
		CreatedAt:    ws.CreatedAt,
		WorktreePath: ws.WorktreePath,
	}
}

// waitIdleResult is the JSON shape returned by workspace_wait_idle.
type waitIdleResult struct {
	Idle          bool      `json:"idle"`
	TimedOut      bool      `json:"timed_out"`
	LastChangedAt time.Time `json:"last_changed_at"`
	ElapsedMs     int64     `json:"elapsed_ms"`
	ThresholdMs   int64     `json:"threshold_ms"`
}

func jsonText(v any) (*mcp.CallToolResult, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("internal: marshaling result: %v", err)), nil
	}
	return mcp.NewToolResultText(string(b)), nil
}

// Register adds all workspace tools and the pane resource template to s.
func Register(s *server.MCPServer, mgr Manager, capture PaneCapture, storeUpd StoreUpdater, defaultThresholdMs int64) {
	// MCP resource: workspace://{id}/pane — returns current pane content as text.
	s.AddResourceTemplate(
		mcp.NewResourceTemplate(
			"workspace://{id}/pane",
			"workspace-pane",
			mcp.WithTemplateDescription("Current tmux pane output for a workspace"),
			mcp.WithTemplateMIMEType("text/plain"),
		),
		func(_ context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
			id := req.Params.Arguments["id"]
			if id == nil {
				id = ""
			}
			ws, err := mgr.Get(fmt.Sprintf("%v", id))
			if err != nil {
				return nil, fmt.Errorf("workspace not found: %v", id)
			}
			content, err := capture.CapturePane(ws.TmuxSession, 200)
			if err != nil {
				return nil, fmt.Errorf("capture failed: %w", err)
			}
			return []mcp.ResourceContents{
				mcp.TextResourceContents{
					URI:      req.Params.URI,
					MIMEType: "text/plain",
					Text:     content,
				},
			}, nil
		},
	)
	rl := newRateLimiter()

	// workspace_list
	s.AddTool(mcp.NewTool("workspace_list",
		mcp.WithDescription("List all workspaces. By default excludes archived ones."),
		mcp.WithBoolean("include_archived",
			mcp.Description("Include archived and orphaned workspaces"),
		),
	), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		includeArchived := req.GetBool("include_archived", false)
		workspaces := mgr.List(includeArchived)
		summaries := make([]workspaceSummary, len(workspaces))
		for i, ws := range workspaces {
			summaries[i] = toSummary(ws)
		}
		return jsonText(summaries)
	})

	// workspace_create
	s.AddTool(mcp.NewTool("workspace_create",
		mcp.WithDescription("Create a new workspace: git worktree + tmux session + Claude Code instance."),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("Slug for the workspace (lowercase alphanumeric and hyphens)"),
		),
		mcp.WithString("branch",
			mcp.Description("Git branch to create or check out (defaults to name)"),
		),
		mcp.WithObject("meta",
			mcp.Description("Freeform string key-value metadata"),
		),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name, err := req.RequireString("name")
		if err != nil {
			return mcp.NewToolResultError("name is required"), nil
		}
		branch := req.GetString("branch", "")

		var meta map[string]string
		if raw, ok := req.GetArguments()["meta"]; ok && raw != nil {
			if m, ok2 := raw.(map[string]any); ok2 {
				meta = make(map[string]string, len(m))
				for k, v := range m {
					meta[k] = fmt.Sprintf("%v", v)
				}
			}
		}

		ws, err := mgr.Create(ctx, workspace.CreateOptions{Name: name, Branch: branch, Meta: meta})
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonText(ws)
	})

	// workspace_archive
	s.AddTool(mcp.NewTool("workspace_archive",
		mcp.WithDescription("Gracefully shut down a workspace. Quits Claude Code, removes the worktree, retains the git branch."),
		mcp.WithString("id",
			mcp.Required(),
			mcp.Description("Workspace ID"),
		),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("id")
		if err != nil {
			return mcp.NewToolResultError("id is required"), nil
		}
		ws, err := mgr.Archive(ctx, id)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonText(ws)
	})

	// workspace_delete
	s.AddTool(mcp.NewTool("workspace_delete",
		mcp.WithDescription("Permanently delete a workspace and its git branch. Destructive and irreversible."),
		mcp.WithString("id",
			mcp.Required(),
			mcp.Description("Workspace ID"),
		),
		mcp.WithBoolean("confirm",
			mcp.Required(),
			mcp.Description("Must be true; returns error if false or absent"),
		),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("id")
		if err != nil {
			return mcp.NewToolResultError("id is required"), nil
		}
		confirm := req.GetBool("confirm", false)
		if err := mgr.Delete(ctx, id, confirm); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonText(map[string]any{"deleted": true, "id": id})
	})

	// workspace_send
	s.AddTool(mcp.NewTool("workspace_send",
		mcp.WithDescription("Send text (a prompt or command) to the Claude Code session in a workspace."),
		mcp.WithString("id",
			mcp.Required(),
			mcp.Description("Workspace ID"),
		),
		mcp.WithString("text",
			mcp.Required(),
			mcp.Description("Text to send"),
		),
		mcp.WithBoolean("press_enter",
			mcp.Description("Append Enter keystroke (default true)"),
		),
	), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("id")
		if err != nil {
			return mcp.NewToolResultError("id is required"), nil
		}
		text, err := req.RequireString("text")
		if err != nil {
			return mcp.NewToolResultError("text is required"), nil
		}
		pressEnter := req.GetBool("press_enter", true)

		ws, err := mgr.Get(id)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("workspace not found: %s", id)), nil
		}
		if ws.Status != store.StatusActive {
			return mcp.NewToolResultError(
				fmt.Sprintf("workspace %s is not active (status: %s)", id, ws.Status),
			), nil
		}

		// Reject text with ASCII control characters (except \n and \t).
		if _, hadInvalid := sanitizeText(text); hadInvalid {
			return mcp.NewToolResultError(
				"text contains invalid ASCII control characters (0x00–0x1f, except \\n and \\t)",
			), nil
		}

		// Rate limiting: max one send per workspace per 200 ms.
		retryAfterMs, ok := rl.check(id)
		if !ok {
			return mcp.NewToolResultError(
				fmt.Sprintf(`{"error":"rate limited","retry_after_ms":%d}`, retryAfterMs),
			), nil
		}

		if err := mgr.SendKeys(id, text, pressEnter); err != nil {
			fmt.Fprintf(os.Stderr, "workspace_send: error: %v\n", err)
			return mcp.NewToolResultError(fmt.Sprintf("send failed: %v", err)), nil
		}

		return jsonText(map[string]bool{"sent": true})
	})

	// workspace_read
	s.AddTool(mcp.NewTool("workspace_read",
		mcp.WithDescription("Capture recent terminal output from a workspace's tmux pane."),
		mcp.WithString("id",
			mcp.Required(),
			mcp.Description("Workspace ID"),
		),
		mcp.WithNumber("lines",
			mcp.Description("Number of lines to capture (default 200, max 2000)"),
		),
	), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("id")
		if err != nil {
			return mcp.NewToolResultError("id is required"), nil
		}
		lines := int(req.GetFloat("lines", 200))
		if lines < 1 {
			lines = 1
		}
		if lines > 2000 {
			lines = 2000
		}

		ws, err := mgr.Get(id)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("workspace not found: %s", id)), nil
		}

		content, err := capture.CapturePane(ws.TmuxSession, lines)
		if err != nil {
			fmt.Fprintf(os.Stderr, "workspace_read: capture error: %v\n", err)
			return mcp.NewToolResultError(fmt.Sprintf("capture failed: %v", err)), nil
		}

		return jsonText(map[string]any{
			"content":     content,
			"captured_at": time.Now().UTC().Format(time.RFC3339),
		})
	})

	// workspace_idle
	s.AddTool(mcp.NewTool("workspace_idle",
		mcp.WithDescription("Check whether a workspace is busy or idle based on pane output change detection."),
		mcp.WithString("id",
			mcp.Required(),
			mcp.Description("Workspace ID"),
		),
		mcp.WithNumber("threshold_ms",
			mcp.Description("Override the configured idle threshold in milliseconds"),
		),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("id")
		if err != nil {
			return mcp.NewToolResultError("id is required"), nil
		}

		threshold := defaultThresholdMs
		if v := req.GetFloat("threshold_ms", 0); v > 0 {
			threshold = int64(v)
		}

		ws, err := mgr.Get(id)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("workspace not found: %s", id)), nil
		}

		status, err := idle.Check(ctx, ws, capture, storeUpd, threshold)
		if err != nil {
			fmt.Fprintf(os.Stderr, "workspace_idle: check error: %v\n", err)
			return mcp.NewToolResultError(fmt.Sprintf("idle check failed: %v", err)), nil
		}

		return jsonText(status)
	})

	// workspace_wait_idle
	s.AddTool(mcp.NewTool("workspace_wait_idle",
		mcp.WithDescription("Block until the workspace is idle or the timeout elapses. "+
			"Polls pane output internally; returns the same shape as workspace_idle plus a timed_out flag."),
		mcp.WithString("id",
			mcp.Required(),
			mcp.Description("Workspace ID"),
		),
		mcp.WithNumber("timeout_ms",
			mcp.Description("Maximum time to wait in milliseconds (default 600000 = 10 min)"),
		),
		mcp.WithNumber("threshold_ms",
			mcp.Description("Idle-stability threshold override in milliseconds"),
		),
		mcp.WithNumber("poll_interval_ms",
			mcp.Description("How often to sample the pane in milliseconds (default 500)"),
		),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("id")
		if err != nil {
			return mcp.NewToolResultError("id is required"), nil
		}

		timeoutMs := req.GetFloat("timeout_ms", 600_000)
		threshold := defaultThresholdMs
		if v := req.GetFloat("threshold_ms", 0); v > 0 {
			threshold = int64(v)
		}
		pollIntervalMs := req.GetFloat("poll_interval_ms", 500)

		ws, err := mgr.Get(id)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("workspace not found: %s", id)), nil
		}
		if ws.Status != store.StatusActive {
			return mcp.NewToolResultError(
				fmt.Sprintf("workspace %s is not active (status: %s)", id, ws.Status),
			), nil
		}

		status, waitErr := idle.WaitUntilIdle(ctx, ws, capture, storeUpd, threshold, int64(timeoutMs), int64(pollIntervalMs))
		timedOut := waitErr != nil
		if waitErr != nil && !errors.Is(waitErr, context.DeadlineExceeded) && !errors.Is(waitErr, context.Canceled) {
			fmt.Fprintf(os.Stderr, "workspace_wait_idle: error: %v\n", waitErr)
			return mcp.NewToolResultError(fmt.Sprintf("wait failed: %v", waitErr)), nil
		}

		return jsonText(waitIdleResult{
			Idle:          status.Idle,
			TimedOut:      timedOut,
			LastChangedAt: status.LastChangedAt,
			ElapsedMs:     status.ElapsedMs,
			ThresholdMs:   status.ThresholdMs,
		})
	})

	// workspace_attach_hint
	s.AddTool(mcp.NewTool("workspace_attach_hint",
		mcp.WithDescription("Return the shell command a human should run to attach to this workspace's tmux session."),
		mcp.WithString("id",
			mcp.Required(),
			mcp.Description("Workspace ID"),
		),
	), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("id")
		if err != nil {
			return mcp.NewToolResultError("id is required"), nil
		}
		ws, err := mgr.Get(id)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("workspace not found: %s", id)), nil
		}
		return jsonText(map[string]string{
			"command": fmt.Sprintf("tmux attach-session -t %s", ws.TmuxSession),
		})
	})
}

// sanitizeText scans for ASCII control characters (0x00–0x1f) excluding \n and \t.
// Returns the stripped string and whether any invalid chars were found.
func sanitizeText(s string) (cleaned string, hadInvalid bool) {
	var b strings.Builder
	for _, r := range s {
		if r == '\n' || r == '\t' {
			b.WriteRune(r)
			continue
		}
		if unicode.IsControl(r) && r < 0x20 {
			hadInvalid = true
			continue
		}
		b.WriteRune(r)
	}
	return b.String(), hadInvalid
}
