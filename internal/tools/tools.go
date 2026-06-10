// Package tools registers all MCP tool handlers. It is the only package that imports
// the MCP library; all business logic lives in the workspace package.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/yeagerd/hangar/internal/idle"
	"github.com/yeagerd/hangar/internal/workspace"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Manager is the interface the tool handlers use to access workspace operations.
type Manager interface {
	Create(ctx context.Context, opts workspace.CreateOptions) (workspace.Workspace, error)
	Delete(ctx context.Context, id string, confirmed bool, force bool) error
	List() []workspace.Workspace
	Get(id string) (workspace.Workspace, error)
	Resolve(input string) (workspace.Workspace, error)
	SendKeys(id string, text string, pressEnter bool) error
}

// PaneCapture is the interface for capturing tmux pane output.
type PaneCapture interface {
	CapturePane(sessionName string, lines int) (string, error)
}

// StoreUpdater is the interface for the idle checker to update workspace state.
type StoreUpdater interface {
	UpdateIdleState(id, hash string, changedAt time.Time) error
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
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Branch       string    `json:"branch"`
	TmuxSession  string    `json:"tmuxSession"`
	CreatedAt    time.Time `json:"createdAt"`
	WorktreePath string    `json:"worktreePath"`
	// Idle status is populated for all workspaces in the list.
	IdleStatus *bool `json:"idleStatus,omitempty"`
}

func toSummary(ws workspace.Workspace) workspaceSummary {
	return workspaceSummary{
		ID:           ws.ID,
		Name:         ws.Name,
		Branch:       ws.Branch,
		TmuxSession:  ws.TmuxSession,
		CreatedAt:    ws.CreatedAt,
		WorktreePath: ws.WorktreePath,
	}
}

// waitIdleMultiResult is the JSON shape returned by workspace_wait_idle.
type waitIdleMultiResult struct {
	TimedOut bool                 `json:"timed_out"`
	Results  map[string]idleEntry `json:"results"`
}

// listWaitResult is the JSON shape returned by workspace_list when a wait flag is set.
type listWaitResult struct {
	TimedOut   bool               `json:"timed_out"`
	Workspaces []workspaceSummary `json:"workspaces"`
}

type idleEntry struct {
	Idle bool `json:"idle"`
}

// readResult is the JSON shape returned by workspace_read.
type readResult struct {
	Content    string `json:"content"`
	CapturedAt string `json:"captured_at"`
	Idle       *bool  `json:"idle"`
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
		mcp.WithDescription("List all workspaces."),
		mcp.WithBoolean("wait_any_idle",
			mcp.Description("Block until at least one workspace is idle, then return the list"),
		),
		mcp.WithBoolean("wait_all_idle",
			mcp.Description("Block until all workspaces are idle, then return the list"),
		),
		mcp.WithNumber("timeout_ms",
			mcp.Description("Maximum wait in milliseconds when a wait flag is set (default 600000 = 10 min)"),
		),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		waitAny := req.GetBool("wait_any_idle", false)
		waitAll := req.GetBool("wait_all_idle", false)
		timeoutMs := int64(req.GetFloat("timeout_ms", 600_000))

		if waitAny && waitAll {
			return mcp.NewToolResultError("wait_any_idle and wait_all_idle are mutually exclusive"), nil
		}

		workspaces := mgr.List()

		wsStates := make([]idle.WorkspaceState, 0, len(workspaces))
		for _, ws := range workspaces {
			wsStates = append(wsStates, idle.WorkspaceState{
				ID: ws.ID, Name: ws.Name, TmuxSession: ws.TmuxSession,
				LastCaptureHash: ws.LastCaptureHash, LastChangedAt: ws.LastChangedAt,
			})
		}

		if waitAny || waitAll {
			mode := "all"
			if waitAny {
				mode = "any"
			}
			idleMap, timedOut := idle.WaitUntilIdleMulti(ctx, wsStates, capture, storeUpd, defaultThresholdMs, timeoutMs, mode)
			summaries := make([]workspaceSummary, len(workspaces))
			for i, ws := range workspaces {
				sum := toSummary(ws)
				if isIdle, ok := idleMap[ws.ID]; ok {
					sum.IdleStatus = &isIdle
				}
				summaries[i] = sum
			}
			return jsonText(listWaitResult{TimedOut: timedOut, Workspaces: summaries})
		}

		idleMap := idle.IsIdle(ctx, wsStates, capture, defaultThresholdMs, 0)

		summaries := make([]workspaceSummary, len(workspaces))
		for i, ws := range workspaces {
			sum := toSummary(ws)
			if is, ok := idleMap[ws.ID]; ok {
				idleStatus := is.Idle
				sum.IdleStatus = &idleStatus
			}
			summaries[i] = sum
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

	// workspace_delete
	s.AddTool(mcp.NewTool("workspace_delete",
		mcp.WithDescription("Permanently delete a workspace and its git branch. Destructive and irreversible."),
		mcp.WithString("id",
			mcp.Required(),
			mcp.Description("Workspace ID, name, or unique prefix of either"),
		),
		mcp.WithBoolean("confirm",
			mcp.Required(),
			mcp.Description("Must be true; returns error if false or absent"),
		),
		mcp.WithBoolean("force",
			mcp.Description("Skip dirty/unpushed branch safety check"),
		),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("id")
		if err != nil {
			return mcp.NewToolResultError("id is required"), nil
		}
		confirm := req.GetBool("confirm", false)
		force := req.GetBool("force", false)
		resolved, err := mgr.Resolve(id)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if err := mgr.Delete(ctx, resolved.ID, confirm, force); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonText(map[string]any{"deleted": true, "id": resolved.ID})
	})

	// workspace_send
	s.AddTool(mcp.NewTool("workspace_send",
		mcp.WithDescription("Send text (a prompt or command) to the Claude Code session in a workspace."),
		mcp.WithString("id",
			mcp.Required(),
			mcp.Description("Workspace ID, name, or unique prefix of either"),
		),
		mcp.WithString("text",
			mcp.Required(),
			mcp.Description("Text to send"),
		),
		mcp.WithBoolean("press_enter",
			mcp.Description("Append Enter keystroke (default true)"),
			mcp.DefaultBool(true),
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

		ws, err := mgr.Resolve(id)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("workspace not found: %s", id)), nil
		}

		// Reject text with ASCII control characters (except \n and \t).
		if _, hadInvalid := sanitizeText(text); hadInvalid {
			return mcp.NewToolResultError(
				"text contains invalid ASCII control characters (0x00–0x1f, except \\n and \\t)",
			), nil
		}

		// Rate limiting: max one send per workspace per 200 ms.
		retryAfterMs, ok := rl.check(ws.ID)
		if !ok {
			return mcp.NewToolResultError(
				fmt.Sprintf(`{"error":"rate limited","retry_after_ms":%d}`, retryAfterMs),
			), nil
		}

		if err := mgr.SendKeys(ws.ID, text, pressEnter); err != nil {
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
			mcp.Description("Workspace ID, name, or unique prefix of either"),
		),
		mcp.WithNumber("lines",
			mcp.Description("Number of lines to capture (default 200, max 2000)"),
		),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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

		ws, err := mgr.Resolve(id)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("workspace not found: %s", id)), nil
		}

		content, err := capture.CapturePane(ws.TmuxSession, lines)
		if err != nil {
			fmt.Fprintf(os.Stderr, "workspace_read: capture error: %v\n", err)
			return mcp.NewToolResultError(fmt.Sprintf("capture failed: %v", err)), nil
		}

		wsState := idle.WorkspaceState{
			ID: ws.ID, Name: ws.Name, TmuxSession: ws.TmuxSession,
			LastCaptureHash: ws.LastCaptureHash, LastChangedAt: ws.LastChangedAt,
		}
		idleMap := idle.IsIdle(ctx, []idle.WorkspaceState{wsState}, capture, defaultThresholdMs, 0)
		var idleStatus *bool
		if status, ok := idleMap[ws.ID]; ok {
			v := status.Idle
			idleStatus = &v
		} else {
			fmt.Fprintf(os.Stderr, "workspace_read: idle check failed for %s\n", id)
		}

		return jsonText(readResult{
			Content:    content,
			CapturedAt: time.Now().UTC().Format(time.RFC3339),
			Idle:       idleStatus,
		})
	})

	// workspace_wait_idle
	s.AddTool(mcp.NewTool("workspace_wait_idle",
		mcp.WithDescription("Block until the specified workspaces are idle or the timeout elapses. "+
			"Returns a map of workspace ID → idle status."),
		mcp.WithArray("ids",
			mcp.Required(),
			mcp.Description("List of workspace IDs to watch"),
			mcp.WithStringItems(),
		),
		mcp.WithString("mode",
			mcp.Description(`"all" (default): wait until every workspace is idle. "any": return as soon as at least one is idle.`),
		),
		mcp.WithNumber("timeout_ms",
			mcp.Description("Maximum time to wait in milliseconds (default 600000 = 10 min)"),
		),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		rawIDs, ok := req.GetArguments()["ids"]
		if !ok || rawIDs == nil {
			return mcp.NewToolResultError("ids is required"), nil
		}
		idsRaw, ok := rawIDs.([]any)
		if !ok || len(idsRaw) == 0 {
			return mcp.NewToolResultError("ids must be a non-empty array of strings"), nil
		}
		ids := make([]string, len(idsRaw))
		for i, v := range idsRaw {
			s, ok := v.(string)
			if !ok {
				return mcp.NewToolResultError(fmt.Sprintf("ids[%d] must be a string", i)), nil
			}
			ids[i] = s
		}

		mode := req.GetString("mode", "all")
		if mode != "all" && mode != "any" {
			return mcp.NewToolResultError(`mode must be "all" or "any"`), nil
		}
		timeoutMs := int64(req.GetFloat("timeout_ms", 600_000))

		wsStates := make([]idle.WorkspaceState, 0, len(ids))
		for _, id := range ids {
			ws, err := mgr.Resolve(id)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("workspace not found: %s", id)), nil
			}
			wsStates = append(wsStates, idle.WorkspaceState{
				ID: ws.ID, Name: ws.Name, TmuxSession: ws.TmuxSession,
				LastCaptureHash: ws.LastCaptureHash, LastChangedAt: ws.LastChangedAt,
			})
		}

		idleMap, timedOut := idle.WaitUntilIdleMulti(ctx, wsStates, capture, storeUpd, defaultThresholdMs, timeoutMs, mode)

		resultItems := make(map[string]idleEntry, len(idleMap))
		for id, isIdle := range idleMap {
			resultItems[id] = idleEntry{Idle: isIdle}
		}
		return jsonText(waitIdleMultiResult{TimedOut: timedOut, Results: resultItems})
	})

	// workspace_attach_hint
	s.AddTool(mcp.NewTool("workspace_attach_hint",
		mcp.WithDescription("Return the shell command a human should run to attach to this workspace's tmux session."),
		mcp.WithString("id",
			mcp.Required(),
			mcp.Description("Workspace ID, name, or unique prefix of either"),
		),
	), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("id")
		if err != nil {
			return mcp.NewToolResultError("id is required"), nil
		}
		ws, err := mgr.Resolve(id)
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
