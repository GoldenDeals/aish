package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The test binary is also the server that pings the client. Before
// TestMain, as the stub of client_test.go.
func init() {
	if os.Getenv("AISH_MCP_PING_STUB") == "1" {
		pingStub()
		os.Exit(0)
	}
}

// pingStub is an MCP server on stdio with one tool, work, logging as
// clientStub does. The first server to create $AISH_MCP_DEAF stops reading
// its input after the handshake and, on SIGUSR1, asks the client for a ping.
func pingStub() {
	usr1 := make(chan os.Signal, 1)
	signal.Notify(usr1, syscall.SIGUSR1)
	logf := func(format string, args ...any) {
		f, err := os.OpenFile(os.Getenv("AISH_MCP_LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(f, format+"\n", args...)
			f.Close()
		}
	}
	logf("start")
	deaf := false
	if f, err := os.OpenFile(os.Getenv("AISH_MCP_DEAF"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err == nil {
		f.Close()
		deaf = true
	}
	reply := func(id json.RawMessage, result string) {
		fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":%s}`+"\n", id, result)
	}
	in := bufio.NewScanner(os.Stdin)
	for in.Scan() {
		var m stubMessage
		json.Unmarshal(in.Bytes(), &m)
		switch m.Method {
		case "initialize":
			reply(m.ID, `{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"stub"}}`)
		case "tools/list":
			reply(m.ID, `{"tools":[{"name":"work","inputSchema":{"type":"object"}}]}`)
			if deaf {
				select {
				case <-usr1:
				case <-time.After(time.Minute):
					return
				}
				fmt.Println(`{"jsonrpc":"2.0","id":"p1","method":"ping"}`)
				logf("ping")
				time.Sleep(time.Minute) // until the client stops it
				return
			}
		case "tools/call":
			reply(m.ID, `{"content":[{"type":"text","text":"done"}]}`)
		}
	}
}

// The reply to a server's ping that the server does not take is left to it
// as a notification is: the call stuck behind it gives the server up at its
// timeout, and the next call gets a new server.
func TestPingUnread(t *testing.T) {
	dir := t.TempDir()
	exe, _ := os.Executable()
	m := NewManager(map[string]Server{"stub": {Command: exe, Timeout: 1, Env: map[string]string{
		"AISH_MCP_PING_STUB": "1",
		"AISH_MCP_LOG":       filepath.Join(dir, "log"),
		"AISH_MCP_DEAF":      filepath.Join(dir, "deaf"),
	}}}, filepath.Join(dir, "cache"))
	t.Cleanup(m.Close)
	ctx := context.Background()
	if res := m.List(ctx, true); len(res.Tools) != 1 || len(res.Errors) != 0 {
		t.Fatalf("listing: %+v", res)
	}
	s := m.servers[0]
	s.mu.Lock()
	c := s.conn.(*stdio)
	s.mu.Unlock()

	// The server's input is full when it asks: the reply will not get in.
	fillInput(t, c)
	if err := c.cmd.Process.Signal(syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(cancelTimeout + 5*time.Second); !c.orphaned() && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}

	start := time.Now()
	if _, err := m.Call(ctx, "stub_work", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("call behind the reply: %v", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("gave up after %v, want about 1s", d)
	}
	if c.alive() {
		t.Fatal("a server that does not take the reply to its ping is kept")
	}
	select {
	case <-c.dead:
	case <-time.After(5 * time.Second):
		t.Fatal("the server is not stopped")
	}
	raw, err := m.Call(ctx, "stub_work", nil)
	if err != nil {
		t.Fatalf("next call: %v", err)
	}
	if out, _ := Format(raw); out != "done" {
		t.Errorf("next call: %q", out)
	}
	if got := strings.Join(logLines(dir), " "); got != "start ping start" {
		t.Errorf("log %q, want a start, the ping and another start", got)
	}
}

// A call to an HTTP server may start it, find the session forgotten, start
// a new one and repeat the call there: the client waits for all of it.
func TestHTTPTimeout(t *testing.T) {
	srv := httptest.NewServer(&httpStub{})
	t.Cleanup(srv.Close)
	m := NewManager(map[string]Server{"web": {URL: srv.URL}}, t.TempDir())
	t.Cleanup(m.Close)
	res := m.List(context.Background(), true)
	if len(res.Tools) != 1 || len(res.Errors) != 0 {
		t.Fatalf("listing: %+v", res)
	}
	if got, want := res.Tools[0].Timeout, 2*(startTimeout+callTimeout); got != want {
		t.Errorf("timeout %v, want %v", got, want)
	}
}

// A server that exited and was waited for is not signalled when it is
// closed: its pid, the id of its process group, may be another group's.
func TestCloseExited(t *testing.T) {
	dir := t.TempDir()
	m := clientStubManager(t, dir, 0)
	s := m.servers[0]
	s.mu.Lock()
	c := s.conn.(*stdio)
	s.mu.Unlock()
	if err := c.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.dead:
	case <-time.After(5 * time.Second):
		t.Fatal("the server did not exit")
	}

	// The group its pid names now.
	other := exec.Command("sleep", "60")
	other.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() {
		other.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		other.Process.Kill()
		<-exited
	})
	c.cmd.Process = other.Process

	m.Close()
	select {
	case <-exited:
		t.Fatal("closing a server that exited signalled the group of its pid")
	case <-time.After(500 * time.Millisecond):
	}
}
