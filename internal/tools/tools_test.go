package tools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/yeagerd/hangar/internal/workspace"
)

// ---- mocks ----

type mockManager struct {
	workspaces  []workspace.Workspace
	createErr   error
	deleteErr   error
	sendKeysErr error
	getErr      error
}

func (m *mockManager) Create(_ context.Context, opts workspace.CreateOptions) (workspace.Workspace, error) {
	if m.createErr != nil {
		return workspace.Workspace{}, m.createErr
	}
	ws := workspace.Workspace{
		ID: "ws-1", Name: opts.Name, Branch: opts.Branch,
		TmuxSession: "harness-" + opts.Name, CreatedAt: time.Now(),
	}
	m.workspaces = append(m.workspaces, ws)
	return ws, nil
}

func (m *mockManager) Delete(_ context.Context, id string, confirmed bool, _ bool) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	if !confirmed {
		return workspace.ErrDeleteNotConfirmed
	}
	for i, ws := range m.workspaces {
		if ws.ID == id {
			m.workspaces = append(m.workspaces[:i], m.workspaces[i+1:]...)
			return nil
		}
	}
	return workspace.ErrNotFound
}

func (m *mockManager) List() []workspace.Workspace {
	return m.workspaces
}

func (m *mockManager) Get(id string) (workspace.Workspace, error) {
	if m.getErr != nil {
		return workspace.Workspace{}, m.getErr
	}
	for _, ws := range m.workspaces {
		if ws.ID == id {
			return ws, nil
		}
	}
	return workspace.Workspace{}, workspace.ErrNotFound
}

func (m *mockManager) Resolve(input string) (workspace.Workspace, error) {
	// Exact ID or name first.
	for _, ws := range m.workspaces {
		if ws.ID == input || ws.Name == input {
			return ws, nil
		}
	}
	// Prefix match.
	var matches []workspace.Workspace
	seen := make(map[string]bool)
	for _, ws := range m.workspaces {
		if !seen[ws.ID] && (strings.HasPrefix(ws.ID, input) || strings.HasPrefix(ws.Name, input)) {
			matches = append(matches, ws)
			seen[ws.ID] = true
		}
	}
	switch len(matches) {
	case 0:
		return workspace.Workspace{}, workspace.ErrNotFound
	case 1:
		return matches[0], nil
	default:
		return workspace.Workspace{}, fmt.Errorf("%w: %q matches multiple workspaces", workspace.ErrAmbiguous, input)
	}
}

func (m *mockManager) SendKeys(_ string, _ string, _ bool) error { return m.sendKeysErr }

type mockPaneCapture struct {
	content string
	err     error
}

func (c *mockPaneCapture) CapturePane(_ string, _ int) (string, error) {
	return c.content, c.err
}

type mockStoreUpdater struct {
	calls []struct {
		id   string
		hash string
	}
}

func (u *mockStoreUpdater) UpdateIdleState(id, hash string, _ time.Time) error {
	u.calls = append(u.calls, struct {
		id   string
		hash string
	}{id, hash})
	return nil
}

// ---- helpers ----

func newTestServer(mgr Manager, cap PaneCapture, upd StoreUpdater) *mcpserver.MCPServer {
	s := mcpserver.NewMCPServer("test", "0.1.0", mcpserver.WithToolCapabilities(true))
	Register(s, mgr, cap, upd, 50)
	return s
}

func callTool(t *testing.T, s *mcpserver.MCPServer, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	tool := s.GetTool(name)
	require.NotNil(t, tool, "tool %q not registered", name)
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args
	result, err := tool.Handler(context.Background(), req)
	require.NoError(t, err)
	return result
}

func textContent(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	require.NotEmpty(t, result.Content)
	tc, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok, "expected TextContent")
	return tc.Text
}

func paneHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", sum)
}

// ---- tests ----

func TestWorkspaceList_Empty(t *testing.T) {
	s := newTestServer(&mockManager{}, &mockPaneCapture{}, &mockStoreUpdater{})
	result := callTool(t, s, "workspace_list", nil)
	assert.False(t, result.IsError)
	var list []any
	require.NoError(t, json.Unmarshal([]byte(textContent(t, result)), &list))
	assert.Empty(t, list)
}

func TestWorkspaceList_AlwaysChecksIdle(t *testing.T) {
	content := "stable\n"
	h := paneHash(content)
	ws := workspace.Workspace{
		ID: "ws-1", Name: "myws", TmuxSession: "harness-myws",
		LastCaptureHash: h, LastChangedAt: time.Now().Add(-10 * time.Second),
	}
	mgr := &mockManager{workspaces: []workspace.Workspace{ws}}
	cap := &mockPaneCapture{content: content}
	upd := &mockStoreUpdater{}
	s := newTestServer(mgr, cap, upd)

	result := callTool(t, s, "workspace_list", nil)
	assert.False(t, result.IsError, textContent(t, result))

	var summaries []workspaceSummary
	require.NoError(t, json.Unmarshal([]byte(textContent(t, result)), &summaries))
	require.Len(t, summaries, 1)
	require.NotNil(t, summaries[0].IdleStatus)
	assert.True(t, *summaries[0].IdleStatus)
}

func TestWorkspaceList_WaitAnyIdle(t *testing.T) {
	content := "stable\n"
	h := paneHash(content)
	ws := workspace.Workspace{
		ID: "ws-1", Name: "myws", TmuxSession: "harness-myws",
		LastCaptureHash: h, LastChangedAt: time.Now().Add(-10 * time.Second),
	}
	mgr := &mockManager{workspaces: []workspace.Workspace{ws}}
	cap := &mockPaneCapture{content: content}
	upd := &mockStoreUpdater{}
	s := newTestServer(mgr, cap, upd)

	result := callTool(t, s, "workspace_list", map[string]any{
		"wait_any_idle": true,
		"timeout_ms":    5000,
	})
	assert.False(t, result.IsError, textContent(t, result))
	var out listWaitResult
	require.NoError(t, json.Unmarshal([]byte(textContent(t, result)), &out))
	assert.False(t, out.TimedOut)
	require.Len(t, out.Workspaces, 1)
	require.NotNil(t, out.Workspaces[0].IdleStatus)
	assert.True(t, *out.Workspaces[0].IdleStatus)
}

func TestWorkspaceList_WaitAllIdle_Timeout(t *testing.T) {
	call := 0
	ws := workspace.Workspace{
		ID: "ws-1", Name: "myws", TmuxSession: "harness-myws",
	}
	capFunc := &funcCapture{fn: func() string {
		call++
		return fmt.Sprintf("line %d\n", call)
	}}
	mgr := &mockManager{workspaces: []workspace.Workspace{ws}}
	upd := &mockStoreUpdater{}
	s := newTestServer(mgr, capFunc, upd)

	result := callTool(t, s, "workspace_list", map[string]any{
		"wait_all_idle": true,
		"timeout_ms":    150,
	})
	assert.False(t, result.IsError, textContent(t, result))
	var out listWaitResult
	require.NoError(t, json.Unmarshal([]byte(textContent(t, result)), &out))
	assert.True(t, out.TimedOut)
	require.Len(t, out.Workspaces, 1)
	require.NotNil(t, out.Workspaces[0].IdleStatus)
	assert.False(t, *out.Workspaces[0].IdleStatus)
}

func TestWorkspaceList_BothWaitFlags_Error(t *testing.T) {
	s := newTestServer(&mockManager{}, &mockPaneCapture{}, &mockStoreUpdater{})
	result := callTool(t, s, "workspace_list", map[string]any{
		"wait_any_idle": true,
		"wait_all_idle": true,
	})
	assert.True(t, result.IsError)
}

func TestWorkspaceCreate_Happy(t *testing.T) {
	mgr := &mockManager{}
	s := newTestServer(mgr, &mockPaneCapture{}, &mockStoreUpdater{})
	result := callTool(t, s, "workspace_create", map[string]any{"name": "myws"})
	assert.False(t, result.IsError, textContent(t, result))
	require.Len(t, mgr.workspaces, 1)
	assert.Equal(t, "myws", mgr.workspaces[0].Name)
}

func TestWorkspaceCreate_MissingName(t *testing.T) {
	s := newTestServer(&mockManager{}, &mockPaneCapture{}, &mockStoreUpdater{})
	result := callTool(t, s, "workspace_create", map[string]any{})
	assert.True(t, result.IsError)
}

func TestWorkspaceDelete_Confirmed(t *testing.T) {
	mgr := &mockManager{workspaces: []workspace.Workspace{
		{ID: "ws-1", Name: "myws"},
	}}
	s := newTestServer(mgr, &mockPaneCapture{}, &mockStoreUpdater{})
	result := callTool(t, s, "workspace_delete", map[string]any{"id": "ws-1", "confirm": true})
	assert.False(t, result.IsError, textContent(t, result))
	assert.Empty(t, mgr.workspaces)
}

func TestWorkspaceDelete_NotConfirmed(t *testing.T) {
	mgr := &mockManager{workspaces: []workspace.Workspace{
		{ID: "ws-1", Name: "myws"},
	}}
	s := newTestServer(mgr, &mockPaneCapture{}, &mockStoreUpdater{})
	result := callTool(t, s, "workspace_delete", map[string]any{"id": "ws-1", "confirm": false})
	assert.True(t, result.IsError)
	assert.Len(t, mgr.workspaces, 1)
}

func TestWorkspaceDelete_Force(t *testing.T) {
	mgr := &mockManager{workspaces: []workspace.Workspace{
		{ID: "ws-1", Name: "myws"},
	}}
	s := newTestServer(mgr, &mockPaneCapture{}, &mockStoreUpdater{})
	result := callTool(t, s, "workspace_delete", map[string]any{
		"id": "ws-1", "confirm": true, "force": true,
	})
	assert.False(t, result.IsError, textContent(t, result))
	assert.Empty(t, mgr.workspaces)
}

func TestWorkspaceSend_Happy(t *testing.T) {
	mgr := &mockManager{workspaces: []workspace.Workspace{
		{ID: "ws-1", Name: "myws", TmuxSession: "harness-myws"},
	}}
	s := newTestServer(mgr, &mockPaneCapture{}, &mockStoreUpdater{})
	result := callTool(t, s, "workspace_send", map[string]any{"id": "ws-1", "text": "hello"})
	assert.False(t, result.IsError, textContent(t, result))
}

func TestWorkspaceSend_ControlChars(t *testing.T) {
	mgr := &mockManager{workspaces: []workspace.Workspace{
		{ID: "ws-1", Name: "myws"},
	}}
	s := newTestServer(mgr, &mockPaneCapture{}, &mockStoreUpdater{})
	result := callTool(t, s, "workspace_send", map[string]any{"id": "ws-1", "text": "hello\x01world"})
	assert.True(t, result.IsError)
}

func TestWorkspaceRead_Happy(t *testing.T) {
	mgr := &mockManager{workspaces: []workspace.Workspace{
		{ID: "ws-1", Name: "myws", TmuxSession: "harness-myws"},
	}}
	cap := &mockPaneCapture{content: "line1\nline2\n"}
	s := newTestServer(mgr, cap, &mockStoreUpdater{})
	result := callTool(t, s, "workspace_read", map[string]any{"id": "ws-1"})
	assert.False(t, result.IsError, textContent(t, result))
	var out readResult
	require.NoError(t, json.Unmarshal([]byte(textContent(t, result)), &out))
	assert.Equal(t, "line1\nline2\n", out.Content)
	assert.NotEmpty(t, out.CapturedAt)
	require.NotNil(t, out.Idle)
	assert.True(t, *out.Idle)
}

func TestWorkspaceRead_IdleTrue(t *testing.T) {
	content := "stable\n"
	h := paneHash(content)
	ws := workspace.Workspace{
		ID: "ws-1", Name: "myws", TmuxSession: "harness-myws",
		LastCaptureHash: h, LastChangedAt: time.Now().Add(-10 * time.Second),
	}
	mgr := &mockManager{workspaces: []workspace.Workspace{ws}}
	cap := &mockPaneCapture{content: content}
	upd := &mockStoreUpdater{}
	s := newTestServer(mgr, cap, upd)

	result := callTool(t, s, "workspace_read", map[string]any{"id": "ws-1"})
	assert.False(t, result.IsError, textContent(t, result))
	var out readResult
	require.NoError(t, json.Unmarshal([]byte(textContent(t, result)), &out))
	assert.Equal(t, content, out.Content)
	assert.NotEmpty(t, out.CapturedAt)
	require.NotNil(t, out.Idle)
	assert.True(t, *out.Idle)
}

func TestWorkspaceAttachHint(t *testing.T) {
	mgr := &mockManager{workspaces: []workspace.Workspace{
		{ID: "ws-1", Name: "myws", TmuxSession: "harness-myws"},
	}}
	s := newTestServer(mgr, &mockPaneCapture{}, &mockStoreUpdater{})
	result := callTool(t, s, "workspace_attach_hint", map[string]any{"id": "ws-1"})
	assert.False(t, result.IsError, textContent(t, result))
	var out map[string]string
	require.NoError(t, json.Unmarshal([]byte(textContent(t, result)), &out))
	assert.Equal(t, "tmux attach-session -t harness-myws", out["command"])
}

// perSessionCapture returns fixed content keyed by tmux session name.
type perSessionCapture struct {
	contents map[string]string
}

func (c *perSessionCapture) CapturePane(session string, _ int) (string, error) {
	if content, ok := c.contents[session]; ok {
		return content, nil
	}
	return "", nil
}

func TestWorkspaceWaitIdle_SingleWorkspace(t *testing.T) {
	content := "stable\n"
	h := paneHash(content)
	ws := workspace.Workspace{
		ID: "ws-1", Name: "myws", TmuxSession: "harness-myws",
		LastCaptureHash: h, LastChangedAt: time.Now().Add(-10 * time.Second),
	}
	mgr := &mockManager{workspaces: []workspace.Workspace{ws}}
	cap := &mockPaneCapture{content: content}
	upd := &mockStoreUpdater{}
	s := newTestServer(mgr, cap, upd)

	result := callTool(t, s, "workspace_wait_idle", map[string]any{
		"ids":        []any{"ws-1"},
		"timeout_ms": 5000,
	})
	assert.False(t, result.IsError, textContent(t, result))
	var out waitIdleMultiResult
	require.NoError(t, json.Unmarshal([]byte(textContent(t, result)), &out))
	assert.False(t, out.TimedOut)
	require.Contains(t, out.Results, "ws-1")
	assert.True(t, out.Results["ws-1"].Idle)
}

func TestWorkspaceWaitIdle_ModeAll_BothIdle(t *testing.T) {
	content := "stable\n"
	h := paneHash(content)
	ws1 := workspace.Workspace{
		ID: "ws-1", Name: "ws1", TmuxSession: "s1",
		LastCaptureHash: h, LastChangedAt: time.Now().Add(-10 * time.Second),
	}
	ws2 := workspace.Workspace{
		ID: "ws-2", Name: "ws2", TmuxSession: "s2",
		LastCaptureHash: h, LastChangedAt: time.Now().Add(-10 * time.Second),
	}
	mgr := &mockManager{workspaces: []workspace.Workspace{ws1, ws2}}
	cap := &perSessionCapture{contents: map[string]string{"s1": content, "s2": content}}
	upd := &mockStoreUpdater{}
	s := newTestServer(mgr, cap, upd)

	result := callTool(t, s, "workspace_wait_idle", map[string]any{
		"ids":  []any{"ws-1", "ws-2"},
		"mode": "all",
	})
	assert.False(t, result.IsError, textContent(t, result))
	var out waitIdleMultiResult
	require.NoError(t, json.Unmarshal([]byte(textContent(t, result)), &out))
	assert.False(t, out.TimedOut)
	require.Contains(t, out.Results, "ws-1")
	assert.True(t, out.Results["ws-1"].Idle)
	require.Contains(t, out.Results, "ws-2")
	assert.True(t, out.Results["ws-2"].Idle)
}

func TestWorkspaceWaitIdle_ModeAny_OneIdle(t *testing.T) {
	idleContent := "stable\n"
	idleHash := paneHash(idleContent)
	ws1 := workspace.Workspace{
		ID: "ws-1", Name: "ws1", TmuxSession: "s1",
		LastCaptureHash: idleHash, LastChangedAt: time.Now().Add(-10 * time.Second),
	}
	ws2 := workspace.Workspace{
		// No prior hash — first check records the hash, elapsed < threshold → not idle.
		ID: "ws-2", Name: "ws2", TmuxSession: "s2",
	}
	mgr := &mockManager{workspaces: []workspace.Workspace{ws1, ws2}}
	cap := &perSessionCapture{contents: map[string]string{
		"s1": idleContent,
		"s2": "active work\n",
	}}
	upd := &mockStoreUpdater{}
	s := newTestServer(mgr, cap, upd)

	result := callTool(t, s, "workspace_wait_idle", map[string]any{
		"ids":  []any{"ws-1", "ws-2"},
		"mode": "any",
	})
	assert.False(t, result.IsError, textContent(t, result))
	var out waitIdleMultiResult
	require.NoError(t, json.Unmarshal([]byte(textContent(t, result)), &out))
	assert.False(t, out.TimedOut)
	require.Contains(t, out.Results, "ws-1")
	assert.True(t, out.Results["ws-1"].Idle)
	require.Contains(t, out.Results, "ws-2")
	assert.False(t, out.Results["ws-2"].Idle)
}

func TestWorkspaceWaitIdle_Timeout(t *testing.T) {
	// Content changes on every call → never idle. Timeout fires before the 500 ms poll tick.
	call := 0
	ws := workspace.Workspace{
		ID: "ws-1", Name: "myws", TmuxSession: "harness-myws",
	}
	capFunc := &funcCapture{fn: func() string {
		call++
		return fmt.Sprintf("line %d\n", call)
	}}
	mgr := &mockManager{workspaces: []workspace.Workspace{ws}}
	upd := &mockStoreUpdater{}
	s := newTestServer(mgr, capFunc, upd)

	result := callTool(t, s, "workspace_wait_idle", map[string]any{
		"ids":        []any{"ws-1"},
		"timeout_ms": 150,
	})
	assert.False(t, result.IsError, textContent(t, result))
	var out waitIdleMultiResult
	require.NoError(t, json.Unmarshal([]byte(textContent(t, result)), &out))
	assert.True(t, out.TimedOut)
	require.Contains(t, out.Results, "ws-1")
	assert.False(t, out.Results["ws-1"].Idle)
}

func TestWorkspaceWaitIdle_NotFound(t *testing.T) {
	s := newTestServer(&mockManager{}, &mockPaneCapture{}, &mockStoreUpdater{})
	result := callTool(t, s, "workspace_wait_idle", map[string]any{"ids": []any{"ghost"}})
	assert.True(t, result.IsError)
}

func TestWorkspaceWaitIdle_EmptyIDs(t *testing.T) {
	s := newTestServer(&mockManager{}, &mockPaneCapture{}, &mockStoreUpdater{})
	result := callTool(t, s, "workspace_wait_idle", map[string]any{"ids": []any{}})
	assert.True(t, result.IsError)
}

func TestWorkspaceWaitIdle_InvalidMode(t *testing.T) {
	mgr := &mockManager{workspaces: []workspace.Workspace{
		{ID: "ws-1", Name: "myws", TmuxSession: "s1"},
	}}
	s := newTestServer(mgr, &mockPaneCapture{}, &mockStoreUpdater{})
	result := callTool(t, s, "workspace_wait_idle", map[string]any{
		"ids":  []any{"ws-1"},
		"mode": "invalid",
	})
	assert.True(t, result.IsError)
}


// funcCapture calls a function to produce each pane snapshot.
type funcCapture struct {
	fn func() string
}

func (f *funcCapture) CapturePane(_ string, _ int) (string, error) {
	return f.fn(), nil
}

func TestSanitizeText(t *testing.T) {
	tests := []struct {
		input   string
		wantBad bool
	}{
		{"hello world", false},
		{"hello\nworld", false},
		{"hello\tworld", false},
		{"hello\x01world", true},
		{"hello\x00world", true},
		{"hello\x1fworld", true},
	}
	for _, tc := range tests {
		_, bad := sanitizeText(tc.input)
		assert.Equal(t, tc.wantBad, bad, "input: %q", tc.input)
	}
}
