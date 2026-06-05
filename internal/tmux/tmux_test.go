package tmux

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockExecutor records the last call and returns a preset response.
type mockExecutor struct {
	out []byte
	err error
	// For verifying the invocation.
	lastCmd  string
	lastArgs []string
}

func (m *mockExecutor) Run(name string, args ...string) ([]byte, error) {
	m.lastCmd = name
	m.lastArgs = args
	return m.out, m.err
}

func TestParseSessionList(t *testing.T) {
	input := "harness-foo\nharness-bar\nother-session\nharness-baz\n"
	got := parseSessionList(input, "harness-")
	assert.Equal(t, []string{"harness-foo", "harness-bar", "harness-baz"}, got)
}

func TestParseSessionListEmpty(t *testing.T) {
	got := parseSessionList("", "harness-")
	assert.Empty(t, got)
}

func TestParseSessionListNoMatch(t *testing.T) {
	got := parseSessionList("other-1\nother-2\n", "harness-")
	assert.Empty(t, got)
}

func TestListSessions_Happy(t *testing.T) {
	m := &mockExecutor{out: []byte("harness-a\nharness-b\nother\n")}
	c := NewWithExecutor(m)
	sessions, err := c.ListSessions("harness-")
	require.NoError(t, err)
	assert.Equal(t, []string{"harness-a", "harness-b"}, sessions)
}

func TestListSessions_NoServer(t *testing.T) {
	// tmux prints "no server running" when server is not up.
	m := &mockExecutor{
		out: []byte("no server running on /tmp/tmux-1000/default"),
		err: errors.New("exit status 1"),
	}
	c := NewWithExecutor(m)
	sessions, err := c.ListSessions("harness-")
	require.NoError(t, err)
	assert.Empty(t, sessions)
}

func TestListSessions_Error(t *testing.T) {
	m := &mockExecutor{out: []byte("unexpected failure"), err: errors.New("exit status 2")}
	c := NewWithExecutor(m)
	_, err := c.ListSessions("harness-")
	assert.Error(t, err)
}

func TestSessionExists_Found(t *testing.T) {
	m := &mockExecutor{out: []byte("harness-foo\nharness-bar\n")}
	c := NewWithExecutor(m)
	ok, err := c.SessionExists("harness-", "foo")
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestSessionExists_NotFound(t *testing.T) {
	m := &mockExecutor{out: []byte("harness-foo\n")}
	c := NewWithExecutor(m)
	ok, err := c.SessionExists("harness-", "bar")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestNewSession_Happy(t *testing.T) {
	m := &mockExecutor{}
	c := NewWithExecutor(m)
	err := c.NewSession("harness-test", "/tmp")
	require.NoError(t, err)
	assert.Equal(t, "tmux", m.lastCmd)
	assert.Equal(t, []string{"new-session", "-d", "-s", "harness-test", "-c", "/tmp"}, m.lastArgs)
}

func TestNewSession_Error(t *testing.T) {
	m := &mockExecutor{out: []byte("duplicate session"), err: errors.New("exit status 1")}
	c := NewWithExecutor(m)
	err := c.NewSession("harness-test", "/tmp")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "harness-test")
}

func TestKillSession_Happy(t *testing.T) {
	m := &mockExecutor{}
	c := NewWithExecutor(m)
	err := c.KillSession("harness-test")
	require.NoError(t, err)
	assert.Equal(t, []string{"kill-session", "-t", "harness-test"}, m.lastArgs)
}

func TestKillSession_NotFound(t *testing.T) {
	// Kill returns nil when session doesn't exist.
	m := &mockExecutor{
		out: []byte("can't find session: harness-test"),
		err: errors.New("exit status 1"),
	}
	c := NewWithExecutor(m)
	err := c.KillSession("harness-test")
	assert.NoError(t, err)
}

func TestKillSession_OtherError(t *testing.T) {
	m := &mockExecutor{out: []byte("permission denied"), err: errors.New("exit status 1")}
	c := NewWithExecutor(m)
	err := c.KillSession("harness-test")
	assert.Error(t, err)
}

func TestSendKeys_Happy(t *testing.T) {
	m := &mockExecutor{}
	c := NewWithExecutor(m)
	err := c.SendKeys("harness-test", "hello", true)
	require.NoError(t, err)
	assert.Equal(t, []string{"send-keys", "-t", "harness-test", "hello", "Enter"}, m.lastArgs)
}

func TestSendKeys_NoEnter(t *testing.T) {
	m := &mockExecutor{}
	c := NewWithExecutor(m)
	err := c.SendKeys("harness-test", "hello", false)
	require.NoError(t, err)
	assert.Equal(t, []string{"send-keys", "-t", "harness-test", "hello"}, m.lastArgs)
}

func TestSendKeys_SessionNotFound(t *testing.T) {
	m := &mockExecutor{
		out: []byte("can't find session: harness-test"),
		err: errors.New("exit status 1"),
	}
	c := NewWithExecutor(m)
	err := c.SendKeys("harness-test", "hello", true)
	assert.ErrorIs(t, err, ErrSessionNotFound)
}

func TestCapturePane_Happy(t *testing.T) {
	m := &mockExecutor{out: []byte("line1\nline2\n")}
	c := NewWithExecutor(m)
	out, err := c.CapturePane("harness-test", 50)
	require.NoError(t, err)
	assert.Equal(t, "line1\nline2\n", out)
	assert.Equal(t, []string{"capture-pane", "-p", "-t", "harness-test", "-S", "-50"}, m.lastArgs)
}

func TestCapturePane_SessionNotFound(t *testing.T) {
	m := &mockExecutor{
		out: []byte("can't find session"),
		err: errors.New("exit status 1"),
	}
	c := NewWithExecutor(m)
	_, err := c.CapturePane("harness-test", 200)
	assert.ErrorIs(t, err, ErrSessionNotFound)
}

func TestRenameSession_Happy(t *testing.T) {
	m := &mockExecutor{}
	c := NewWithExecutor(m)
	err := c.RenameSession("old", "new")
	require.NoError(t, err)
	assert.Equal(t, []string{"rename-session", "-t", "old", "new"}, m.lastArgs)
}

func TestRenameSession_NotFound(t *testing.T) {
	m := &mockExecutor{
		out: []byte("can't find session: old"),
		err: errors.New("exit status 1"),
	}
	c := NewWithExecutor(m)
	err := c.RenameSession("old", "new")
	assert.ErrorIs(t, err, ErrSessionNotFound)
}

func TestRenameSession_Error(t *testing.T) {
	m := &mockExecutor{
		out: []byte("something went wrong"),
		err: fmt.Errorf("exit status 1"),
	}
	c := NewWithExecutor(m)
	err := c.RenameSession("old", "new")
	assert.Error(t, err)
	assert.NotErrorIs(t, err, ErrSessionNotFound)
}
