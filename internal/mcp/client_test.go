package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// The test binary is also the server of the client tests. Before TestMain:
// that one belongs to the stub of mcp_test.go.
func init() {
	if os.Getenv("AISH_MCP_CLIENT_STUB") == "1" {
		clientStub()
		os.Exit(0)
	}
}

// clientStub is an MCP server on stdio with one tool, work. It appends to
// $AISH_MCP_LOG a line per start, per call it leaves unanswered and per
// cancellation. The first server to create $AISH_MCP_DEAF stops reading its
// input after the handshake.
func clientStub() {
	logf := func(format string, args ...any) {
		f, err := os.OpenFile(os.Getenv("AISH_MCP_LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(f, format+"\n", args...)
			f.Close()
		}
	}
	logf("start")
	deaf := false
	if p := os.Getenv("AISH_MCP_DEAF"); p != "" {
		if f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err == nil {
			f.Close()
			deaf = true
		}
	}
	reply := func(id json.RawMessage, result string) {
		fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":%s}`+"\n", id, result)
	}
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 64<<20)
	for in.Scan() {
		var m stubMessage
		json.Unmarshal(in.Bytes(), &m)
		switch m.Method {
		case "initialize":
			reply(m.ID, `{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"stub"}}`)
		case "tools/list":
			reply(m.ID, `{"tools":[{"name":"work","inputSchema":{"type":"object"}}]}`)
			if deaf {
				time.Sleep(time.Hour) // until the client stops it
			}
		case "tools/call":
			if m.Params.Arguments["hang"] == true {
				logf("hang %s", m.ID)
				continue
			}
			reply(m.ID, `{"content":[{"type":"text","text":"done"}]}`)
		case "notifications/cancelled":
			logf("cancelled %s", m.Params.RequestID)
		}
	}
}

type stubMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		Arguments map[string]any  `json:"arguments"`
		RequestID json.RawMessage `json:"requestId"`
	} `json:"params"`
}

func clientStubManager(t *testing.T, dir string, timeout int, env ...string) *Manager {
	t.Helper()
	exe, _ := os.Executable()
	e := map[string]string{"AISH_MCP_CLIENT_STUB": "1", "AISH_MCP_LOG": filepath.Join(dir, "log")}
	for i := 0; i+1 < len(env); i += 2 {
		e[env[i]] = env[i+1]
	}
	m := NewManager(map[string]Server{"stub": {Command: exe, Env: e, Timeout: timeout}}, filepath.Join(dir, "cache"))
	t.Cleanup(m.Close)
	if res := m.List(context.Background(), true); len(res.Tools) != 1 || len(res.Errors) != 0 {
		t.Fatalf("listing: %+v", res)
	}
	return m
}

func logLines(dir string) []string {
	b, _ := os.ReadFile(filepath.Join(dir, "log"))
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// A server that stops reading its input fails the call that writes to it
// at the call's timeout instead of never, and the next call gets a new
// server.
func TestDeafServer(t *testing.T) {
	dir := t.TempDir()
	m := clientStubManager(t, dir, 1, "AISH_MCP_DEAF", filepath.Join(dir, "deaf"))
	ctx := context.Background()
	big := map[string]any{"data": strings.Repeat("x", 2<<20)} // far over a pipe's buffer
	start := time.Now()
	if _, err := m.Call(ctx, "stub_work", big); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("call to a deaf server: %v", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("gave up after %v, want about 1s", d)
	}
	raw, err := m.Call(ctx, "stub_work", big)
	if err != nil {
		t.Fatalf("next call: %v", err)
	}
	if out, _ := Format(raw); out != "done" {
		t.Errorf("next call: %q", out)
	}
	if got := logLines(dir); !reflect.DeepEqual(got, []string{"start", "start"}) {
		t.Errorf("log %q, want two starts", got)
	}
}

// A call nobody waits for anymore is cancelled at the server.
func TestCancelStdio(t *testing.T) {
	dir := t.TempDir()
	m := clientStubManager(t, dir, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := m.Call(ctx, "stub_work", map[string]any{"hang": true}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("hung call: %v", err)
	}
	var hung, cancelled string
	for deadline := time.Now().Add(5 * time.Second); cancelled == "" && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
		for _, l := range logLines(dir) {
			if id, ok := strings.CutPrefix(l, "hang "); ok {
				hung = id
			}
			if id, ok := strings.CutPrefix(l, "cancelled "); ok {
				cancelled = id
			}
		}
	}
	if hung == "" || cancelled != hung {
		t.Errorf("call %q, cancelled %q; log %q", hung, cancelled, logLines(dir))
	}
	// The server lives on for the next call.
	if _, err := m.Call(context.Background(), "stub_work", nil); err != nil {
		t.Errorf("call after it: %v", err)
	}
	if got := logLines(dir); strings.Count(strings.Join(got, "\n"), "start") != 1 {
		t.Errorf("log %q, want one start", got)
	}
}

// httpStub is a Streamable HTTP server that knows one session at a time.
type httpStub struct {
	called, cancelled chan string

	mu      sync.Mutex
	inits   []string // the session each initialize came with
	session string
}

func (h *httpStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var m stubMessage
	json.NewDecoder(r.Body).Decode(&m)
	h.mu.Lock()
	if m.Method == "initialize" {
		h.inits = append(h.inits, r.Header.Get("Mcp-Session-Id"))
		h.session = fmt.Sprintf("s%d", len(h.inits))
		w.Header().Set("Mcp-Session-Id", h.session)
	}
	session := h.session
	h.mu.Unlock()
	if m.Method != "initialize" && r.Header.Get("Mcp-Session-Id") != session {
		http.Error(w, "unknown session", http.StatusNotFound)
		return
	}
	reply := func(result string) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":%s}`, m.ID, result)
	}
	switch m.Method {
	case "initialize":
		reply(`{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"stub"}}`)
	case "tools/list":
		reply(`{"tools":[{"name":"work","inputSchema":{"type":"object"}}]}`)
	case "tools/call":
		if m.Params.Arguments["hang"] == true {
			h.called <- string(m.ID)
			<-r.Context().Done()
			return
		}
		reply(fmt.Sprintf(`{"content":[{"type":"text","text":"done in %s"}]}`, session))
	case "notifications/cancelled":
		h.cancelled <- string(m.Params.RequestID)
		w.WriteHeader(http.StatusAccepted)
	default:
		w.WriteHeader(http.StatusAccepted)
	}
}

func httpStubManager(t *testing.T, h *httpStub) *Manager {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	m := NewManager(map[string]Server{"web": {URL: srv.URL}}, t.TempDir())
	t.Cleanup(m.Close)
	if res := m.List(context.Background(), true); len(res.Tools) != 1 || len(res.Errors) != 0 {
		t.Fatalf("listing: %+v", res)
	}
	return m
}

// A restarted server answers 404 to the old session; the call goes through
// in a new one, and the user never sees the error.
func TestHTTPSessionExpired(t *testing.T) {
	h := &httpStub{}
	m := httpStubManager(t, h)
	h.mu.Lock()
	h.session = "" // the server restarted
	h.mu.Unlock()
	raw, err := m.Call(context.Background(), "web_work", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out, _ := Format(raw); out != "done in s2" {
		t.Errorf("result %q", out)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !reflect.DeepEqual(h.inits, []string{"", ""}) {
		t.Errorf("initialize came with sessions %q, want two without one", h.inits)
	}
}

func TestCancelHTTP(t *testing.T) {
	h := &httpStub{called: make(chan string, 1), cancelled: make(chan string, 1)}
	m := httpStubManager(t, h)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := m.Call(ctx, "web_work", map[string]any{"hang": true}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("hung call: %v", err)
	}
	id := <-h.called
	select {
	case got := <-h.cancelled:
		if got != id {
			t.Errorf("cancelled %q, the call was %q", got, id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no cancellation")
	}
}
