package tools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/articulant/tmux-harness/internal/store"
	"github.com/articulant/tmux-harness/internal/workspace"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- mocks ----

type mockManager struct {
	workspaces   []store.Workspace
	createErr    error
	archiveErr   error
	deleteErr    error
	sendKeysErr  error
	getErr       error
}

func (m *mockManager) Create(_ context.Context, opts workspace.CreateOptions) (store.Workspace, error) {
	if m.createErr != nil {
		return store.Workspace{}, m.createErr
	}
	ws := store.Workspace{
		ID: "ws-1", Name: opts.Name, Branch: opts.Branch,
		TmuxSession: "harness-" + opts.Name, Status: store.StatusActive, CreatedAt: time.Now(),
	}
	m.workspaces = append(m.workspaces, ws)
	return ws, nil
}

func (m *mockManager) Archive(_ context.Context, id string) (store.Workspace, error) {
	if m.archiveErr != nil {
		return store.Workspace{}, m.archiveErr
	}
	for i := range m.workspaces {
		if m.workspaces[i].ID == id {
			m.workspaces[i].Status = store.StatusArchived
			return m.workspaces[i], nil
		}
	}
	return store.Workspace{}, workspace.ErrNotFound
}

func (m *mockManager) Delete(_ context.Context, id string, confirmed bool) error {
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

func (m *mockManager) List(includeArchived bool) []store.Workspace {
	if includeArchived {
		return m.workspaces
	}
	var out []store.Workspace
	for _, ws := range m.workspaces {
		if ws.Status == store.StatusActive {
			out = append(out, ws)
		}
	}
	return out
}

func (m *mockManager) Get(id string) (store.Workspace, error) {
	if m.getErr != nil {
		return store.Workspace{}, m.getErr
	}
	for _, ws := range m.workspaces {
		if ws.ID == id {
			return ws, nil
		}
	}
	return store.Workspace{}, workspace.ErrNotFound
}

func (m *mockManager) GetByName(name string) (store.Workspace, error) {
	for _, ws := range m.workspaces {
		if ws.Name == name {
			return ws, nil
		}
	}
	return store.Workspace{}, workspace.ErrNotFound
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
	data map[string]store.Workspace
}

func (u *mockStoreUpdater) Update(id string, apply func(*store.Workspace)) error {
	if ws, ok := u.data[id]; ok {
		apply(&ws)
		u.data[id] = ws
	}
	return nil
}

func (u *mockStoreUpdater) Get(id string) (store.Workspace, error) {
	if ws, ok := u.data[id]; ok {
		return ws, nil
	}
	return store.Workspace{}, workspace.ErrNotFound
}

// ---- helpers ----

func newTestServer(mgr Manager, cap PaneCapture, upd StoreUpdater) *mcpserver.MCPServer {
	s := mcpserver.NewMCPServer("test", "0.1.0", mcpserver.WithToolCapabilities(true))
	Register(s, mgr, cap, upd, 5000)
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

func TestWorkspaceList_IncludeArchived(t *testing.T) {
	mgr := &mockManager{workspaces: []store.Workspace{
		{ID: "1", Name: "a", Status: store.StatusActive},
		{ID: "2", Name: "b", Status: store.StatusArchived},
	}}
	s := newTestServer(mgr, &mockPaneCapture{}, &mockStoreUpdater{})

	r1 := callTool(t, s, "workspace_list", nil)
	var active []any
	require.NoError(t, json.Unmarshal([]byte(textContent(t, r1)), &active))
	assert.Len(t, active, 1)

	r2 := callTool(t, s, "workspace_list", map[string]any{"include_archived": true})
	var all []any
	require.NoError(t, json.Unmarshal([]byte(textContent(t, r2)), &all))
	assert.Len(t, all, 2)
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

func TestWorkspaceArchive_Happy(t *testing.T) {
	mgr := &mockManager{workspaces: []store.Workspace{
		{ID: "ws-1", Name: "myws", Status: store.StatusActive},
	}}
	s := newTestServer(mgr, &mockPaneCapture{}, &mockStoreUpdater{})
	result := callTool(t, s, "workspace_archive", map[string]any{"id": "ws-1"})
	assert.False(t, result.IsError, textContent(t, result))
	assert.Equal(t, store.StatusArchived, mgr.workspaces[0].Status)
}

func TestWorkspaceArchive_NotFound(t *testing.T) {
	s := newTestServer(&mockManager{}, &mockPaneCapture{}, &mockStoreUpdater{})
	result := callTool(t, s, "workspace_archive", map[string]any{"id": "ghost"})
	assert.True(t, result.IsError)
}

func TestWorkspaceDelete_Confirmed(t *testing.T) {
	mgr := &mockManager{workspaces: []store.Workspace{
		{ID: "ws-1", Name: "myws", Status: store.StatusActive},
	}}
	s := newTestServer(mgr, &mockPaneCapture{}, &mockStoreUpdater{})
	result := callTool(t, s, "workspace_delete", map[string]any{"id": "ws-1", "confirm": true})
	assert.False(t, result.IsError, textContent(t, result))
	assert.Empty(t, mgr.workspaces)
}

func TestWorkspaceDelete_NotConfirmed(t *testing.T) {
	mgr := &mockManager{workspaces: []store.Workspace{
		{ID: "ws-1", Name: "myws", Status: store.StatusActive},
	}}
	s := newTestServer(mgr, &mockPaneCapture{}, &mockStoreUpdater{})
	result := callTool(t, s, "workspace_delete", map[string]any{"id": "ws-1", "confirm": false})
	assert.True(t, result.IsError)
	assert.Len(t, mgr.workspaces, 1)
}

func TestWorkspaceSend_Happy(t *testing.T) {
	mgr := &mockManager{workspaces: []store.Workspace{
		{ID: "ws-1", Name: "myws", Status: store.StatusActive, TmuxSession: "harness-myws"},
	}}
	s := newTestServer(mgr, &mockPaneCapture{}, &mockStoreUpdater{})
	result := callTool(t, s, "workspace_send", map[string]any{"id": "ws-1", "text": "hello"})
	assert.False(t, result.IsError, textContent(t, result))
}

func TestWorkspaceSend_ControlChars(t *testing.T) {
	mgr := &mockManager{workspaces: []store.Workspace{
		{ID: "ws-1", Name: "myws", Status: store.StatusActive},
	}}
	s := newTestServer(mgr, &mockPaneCapture{}, &mockStoreUpdater{})
	result := callTool(t, s, "workspace_send", map[string]any{"id": "ws-1", "text": "hello\x01world"})
	assert.True(t, result.IsError)
}

func TestWorkspaceSend_NotActive(t *testing.T) {
	mgr := &mockManager{workspaces: []store.Workspace{
		{ID: "ws-1", Name: "myws", Status: store.StatusArchived},
	}}
	s := newTestServer(mgr, &mockPaneCapture{}, &mockStoreUpdater{})
	result := callTool(t, s, "workspace_send", map[string]any{"id": "ws-1", "text": "hello"})
	assert.True(t, result.IsError)
}

func TestWorkspaceRead_Happy(t *testing.T) {
	mgr := &mockManager{workspaces: []store.Workspace{
		{ID: "ws-1", Name: "myws", Status: store.StatusActive, TmuxSession: "harness-myws"},
	}}
	cap := &mockPaneCapture{content: "line1\nline2\n"}
	s := newTestServer(mgr, cap, &mockStoreUpdater{})
	result := callTool(t, s, "workspace_read", map[string]any{"id": "ws-1"})
	assert.False(t, result.IsError, textContent(t, result))
	var out map[string]any
	require.NoError(t, json.Unmarshal([]byte(textContent(t, result)), &out))
	assert.Equal(t, "line1\nline2\n", out["content"])
	assert.NotEmpty(t, out["captured_at"])
}

func TestWorkspaceIdle_Idle(t *testing.T) {
	content := "stable\n"
	h := paneHash(content)
	ws := store.Workspace{
		ID: "ws-1", Name: "myws", Status: store.StatusActive, TmuxSession: "harness-myws",
		LastCaptureHash: h, LastChangedAt: time.Now().Add(-10 * time.Second),
	}
	mgr := &mockManager{workspaces: []store.Workspace{ws}}
	cap := &mockPaneCapture{content: content}
	upd := &mockStoreUpdater{data: map[string]store.Workspace{"ws-1": ws}}
	s := newTestServer(mgr, cap, upd)

	result := callTool(t, s, "workspace_idle", map[string]any{"id": "ws-1"})
	assert.False(t, result.IsError, textContent(t, result))
	var out map[string]any
	require.NoError(t, json.Unmarshal([]byte(textContent(t, result)), &out))
	assert.True(t, out["Idle"].(bool))
}

func TestWorkspaceAttachHint(t *testing.T) {
	mgr := &mockManager{workspaces: []store.Workspace{
		{ID: "ws-1", Name: "myws", Status: store.StatusActive, TmuxSession: "harness-myws"},
	}}
	s := newTestServer(mgr, &mockPaneCapture{}, &mockStoreUpdater{})
	result := callTool(t, s, "workspace_attach_hint", map[string]any{"id": "ws-1"})
	assert.False(t, result.IsError, textContent(t, result))
	var out map[string]string
	require.NoError(t, json.Unmarshal([]byte(textContent(t, result)), &out))
	assert.Equal(t, "tmux attach-session -t harness-myws", out["command"])
}

func TestWorkspaceWaitIdle_AlreadyIdle(t *testing.T) {
	content := "stable\n"
	h := paneHash(content)
	ws := store.Workspace{
		ID: "ws-1", Name: "myws", Status: store.StatusActive, TmuxSession: "harness-myws",
		LastCaptureHash: h, LastChangedAt: time.Now().Add(-10 * time.Second),
	}
	mgr := &mockManager{workspaces: []store.Workspace{ws}}
	cap := &mockPaneCapture{content: content}
	upd := &mockStoreUpdater{data: map[string]store.Workspace{"ws-1": ws}}
	s := newTestServer(mgr, cap, upd)

	result := callTool(t, s, "workspace_wait_idle", map[string]any{
		"id":           "ws-1",
		"timeout_ms":   5000,
		"threshold_ms": 200,
	})
	assert.False(t, result.IsError, textContent(t, result))
	var out waitIdleResult
	require.NoError(t, json.Unmarshal([]byte(textContent(t, result)), &out))
	assert.True(t, out.Idle)
	assert.False(t, out.TimedOut)
}

func TestWorkspaceWaitIdle_Timeout(t *testing.T) {
	// Content always changes → never idle.
	call := 0

	ws := store.Workspace{
		ID: "ws-1", Name: "myws", Status: store.StatusActive, TmuxSession: "harness-myws",
	}

	// Build a capture that returns distinct content each call.
	capFunc := &funcCapture{fn: func() string {
		call++
		return fmt.Sprintf("line %d\n", call)
	}}

	mgr := &mockManager{workspaces: []store.Workspace{ws}}
	upd := &mockStoreUpdater{data: map[string]store.Workspace{"ws-1": ws}}
	s := newTestServer(mgr, capFunc, upd)

	result := callTool(t, s, "workspace_wait_idle", map[string]any{
		"id":              "ws-1",
		"timeout_ms":      150,
		"threshold_ms":    200,
		"poll_interval_ms": 40,
	})
	assert.False(t, result.IsError, textContent(t, result))
	var out waitIdleResult
	require.NoError(t, json.Unmarshal([]byte(textContent(t, result)), &out))
	assert.False(t, out.Idle)
	assert.True(t, out.TimedOut)
}

func TestWorkspaceWaitIdle_NotFound(t *testing.T) {
	s := newTestServer(&mockManager{}, &mockPaneCapture{}, &mockStoreUpdater{})
	result := callTool(t, s, "workspace_wait_idle", map[string]any{"id": "ghost"})
	assert.True(t, result.IsError)
}

func TestWorkspaceWaitIdle_NotActive(t *testing.T) {
	mgr := &mockManager{workspaces: []store.Workspace{
		{ID: "ws-1", Name: "myws", Status: store.StatusArchived},
	}}
	s := newTestServer(mgr, &mockPaneCapture{}, &mockStoreUpdater{})
	result := callTool(t, s, "workspace_wait_idle", map[string]any{"id": "ws-1"})
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
		input    string
		wantBad  bool
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
