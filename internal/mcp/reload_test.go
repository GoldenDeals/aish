package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Reload keeps a server whose config is the same as it is, running, stops
// the one gone from the config, and makes one changed anew.
func TestReload(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "cache")
	exe, _ := os.Executable()
	stubAt := func(starts string) Server {
		return Server{Command: exe, Env: map[string]string{"AISH_MCP_STUB": "1", "AISH_MCP_STARTS": filepath.Join(dir, starts)}}
	}
	starts := func(name string) int {
		b, _ := os.ReadFile(filepath.Join(dir, name))
		return strings.Count(string(b), "start")
	}
	names := func(m *Manager) map[string]string {
		got := map[string]string{}
		for _, s := range m.Status().Servers {
			got[s.Name] = s.State
		}
		return got
	}
	ctx := context.Background()
	m := NewManager(map[string]Server{"stub": stubAt("stub"), "gone": stubAt("gone")}, cache)
	t.Cleanup(m.Close)
	if res := m.List(ctx, true); len(res.Tools) != 6 || len(res.Errors) != 0 {
		t.Fatalf("listed %+v", res)
	}
	gone := m.list()[0]
	gone.mu.Lock()
	goneConn := gone.conn
	gone.mu.Unlock()

	// A new one that is not to start: its command waits for a call.
	fresh := Server{Command: "/nonexistent/fresh", EnvCommand: map[string]string{"TOKEN": "echo t"}}
	m.Reload(map[string]Server{"stub": stubAt("stub"), "fresh": fresh})
	if got, want := names(m), map[string]string{"stub": "running", "fresh": "new"}; !reflect.DeepEqual(got, want) {
		t.Errorf("servers %v, want %v", got, want)
	}
	if _, err := m.Call(ctx, "stub_search", map[string]any{"query": "x", "author": "a"}); err != nil {
		t.Errorf("the server kept: %v", err)
	}
	if n := starts("stub"); n != 1 {
		t.Errorf("the server kept started %d times", n)
	}
	if _, err := m.Call(ctx, "gone_search", map[string]any{"query": "x", "author": "a"}); err == nil {
		t.Error("a call to the server gone")
	}
	if _, err := m.ensure(ctx, gone, true); !errors.Is(err, errStopped) {
		t.Errorf("the server gone started: %v", err)
	}
	for deadline := time.Now().Add(5 * time.Second); goneConn.alive(); {
		if time.Now().After(deadline) {
			t.Fatal("the server gone still runs")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := starts("gone"); n != 1 {
		t.Errorf("the server gone started %d times", n)
	}

	// Changed: another server, which knows its tools once it starts.
	changed := stubAt("stub")
	changed.Timeout = 7
	m.Reload(map[string]Server{"stub": changed})
	if got, want := names(m), map[string]string{"stub": "new"}; !reflect.DeepEqual(got, want) {
		t.Errorf("servers %v, want %v", got, want)
	}
	if _, err := m.Call(ctx, "stub_search", map[string]any{"query": "x", "author": "a"}); err != nil {
		t.Errorf("the server changed: %v", err)
	}
	if n := starts("stub"); n != 2 {
		t.Errorf("the server changed started %d times in all", n)
	}
}

// A start under way when the server is let go closes what it gets, and
// leaves the cache to the config that replaced it.
func TestReloadStarting(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "cache")
	exe, _ := os.Executable()
	cfg := Server{Command: exe, Env: map[string]string{"AISH_MCP_STUB": "1", "AISH_MCP_STARTS": filepath.Join(dir, "starts")}}
	m := NewManager(map[string]Server{"stub": cfg}, cache)
	t.Cleanup(m.Close)
	s := m.list()[0]
	st := &startup{done: make(chan struct{})}
	s.mu.Lock()
	s.starting = st
	s.mu.Unlock()
	s.stop()
	m.start(s, st)
	if !errors.Is(st.err, errStopped) || st.conn != nil {
		t.Errorf("start of a server let go: %v, %v", st.conn, st.err)
	}
	if _, err := os.Stat(m.cachePath("stub")); err == nil {
		t.Error("cached the tools of a server let go")
	}
}
