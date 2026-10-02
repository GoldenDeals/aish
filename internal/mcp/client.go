// Package mcp makes the tools of MCP servers ordinary aish tools. The proxy
// owns the server processes for the life of the shell and starts each one
// when it is first needed; `aish tool` and the agent reach them over the
// proxy's socket, and every tool gets a command wrapper on PATH.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"go.yaml.in/yaml/v3"
)

const protocolVersion = "2025-06-18"

// Server is one entry of mcp.yaml: a command speaking MCP on stdio, or the
// URL of a Streamable HTTP endpoint.
type Server struct {
	Command string            `yaml:"command" json:"command,omitempty"`
	Args    []string          `yaml:"args" json:"args,omitempty"`
	Env     map[string]string `yaml:"env" json:"env,omitempty"`
	URL     string            `yaml:"url" json:"url,omitempty"`
	Headers map[string]string `yaml:"headers" json:"headers,omitempty"`
	// Expose is "commands" (the default) or "tools", which also gives the
	// model the tools' schemas.
	Expose string `yaml:"expose" json:"expose,omitempty"`
	// Timeout of a tool call, in seconds.
	Timeout int `yaml:"timeout" json:"timeout,omitempty"`
}

// LoadConfig reads mcp.yaml. A missing file means no servers.
func LoadConfig(path string) (map[string]Server, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f struct {
		Servers map[string]Server `yaml:"servers"`
	}
	if err := yaml.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for name, s := range f.Servers {
		if (s.Command == "") == (s.URL == "") {
			return nil, fmt.Errorf("%s: server %s needs either command or url", path, name)
		}
		if s.Expose != "" && s.Expose != "commands" && s.Expose != "tools" {
			return nil, fmt.Errorf("%s: server %s: expose must be commands or tools", path, name)
		}
	}
	return f.Servers, nil
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  any             `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func (m message) result() (json.RawMessage, error) {
	if m.Error != nil {
		return nil, fmt.Errorf("%s (code %d)", m.Error.Message, m.Error.Code)
	}
	return m.Result, nil
}

// conn is a JSON-RPC connection to one server.
type conn interface {
	call(ctx context.Context, method string, params any) (json.RawMessage, error)
	notify(ctx context.Context, method string) error
	close()
	alive() bool
}

func dial(s Server) (conn, error) {
	if s.URL != "" {
		h := map[string]string{}
		for k, v := range s.Headers {
			h[k] = os.ExpandEnv(v)
		}
		return &httpConn{url: s.URL, headers: h}, nil
	}
	return startStdio(s)
}

// initialize performs the MCP handshake.
func initialize(ctx context.Context, c conn) error {
	_, err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "aish", "version": "0.1"},
	})
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	return c.notify(ctx, "notifications/initialized")
}

type stdio struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	stderr *tail

	mu      sync.Mutex
	next    int
	pending map[string]chan message
	dead    chan struct{}
	err     error
}

func startStdio(s Server) (*stdio, error) {
	cmd := exec.Command(s.Command, s.Args...)
	cmd.Env = os.Environ()
	for k, v := range s.Env {
		cmd.Env = append(cmd.Env, k+"="+os.ExpandEnv(v))
	}
	// Its own process group: npx and the like leave children behind.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c := &stdio{cmd: cmd, stderr: &tail{}, pending: map[string]chan message{}, dead: make(chan struct{})}
	cmd.Stderr = c.stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c.in = in
	go c.read(out)
	return c, nil
}

func (c *stdio) read(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var m message
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		switch {
		case m.Method != "" && m.ID != nil:
			// A request from the server. aish offers no client features.
			reply := message{JSONRPC: "2.0", ID: m.ID}
			if m.Method == "ping" {
				reply.Result = json.RawMessage("{}")
			} else {
				reply.Error = &rpcError{Code: -32601, Message: "method not found"}
			}
			c.write(reply)
		case m.Method == "":
			c.mu.Lock()
			ch := c.pending[string(m.ID)]
			delete(c.pending, string(m.ID))
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		}
	}
	err := c.cmd.Wait()
	c.mu.Lock()
	c.err = fmt.Errorf("server exited (%v)%s", err, c.stderr.String())
	c.mu.Unlock()
	close(c.dead)
}

func (c *stdio) write(m message) error {
	m.JSONRPC = "2.0"
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = c.in.Write(append(b, '\n'))
	return err
}

func (c *stdio) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	c.next++
	id := json.RawMessage(fmt.Sprint(c.next))
	ch := make(chan message, 1)
	c.pending[string(id)] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, string(id))
		c.mu.Unlock()
	}()
	if err := c.write(message{ID: id, Method: method, Params: params}); err != nil {
		return nil, c.failure(err)
	}
	select {
	case m := <-ch:
		return m.result()
	case <-c.dead:
		return nil, c.failure(nil)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *stdio) failure(err error) error {
	select {
	case <-c.dead:
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.err
	default:
		return err
	}
}

func (c *stdio) notify(_ context.Context, method string) error {
	return c.write(message{Method: method})
}

func (c *stdio) alive() bool {
	select {
	case <-c.dead:
		return false
	default:
		return true
	}
}

func (c *stdio) close() {
	c.in.Close()
	if c.cmd.Process != nil {
		syscall.Kill(-c.cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-c.dead:
		case <-time.After(time.Second):
			syscall.Kill(-c.cmd.Process.Pid, syscall.SIGKILL)
		}
	}
}

// tail keeps the end of a server's stderr for error messages.
type tail struct {
	mu sync.Mutex
	b  []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > 2000 {
		t.b = t.b[len(t.b)-2000:]
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if s := strings.TrimSpace(string(t.b)); s != "" {
		return ": " + s
	}
	return ""
}

// httpConn speaks Streamable HTTP: every message is a POST, answered with
// JSON or with an event stream that carries the response.
type httpConn struct {
	url     string
	headers map[string]string

	mu      sync.Mutex
	next    int
	session string
}

func (c *httpConn) post(ctx context.Context, m message) (*http.Response, error) {
	m.JSONRPC = "2.0"
	body, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", protocolVersion)
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	c.mu.Lock()
	if c.session != "" {
		req.Header.Set("Mcp-Session-Id", c.session)
	}
	c.mu.Unlock()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		resp.Body.Close()
		return nil, fmt.Errorf("%s: %s %s", c.url, resp.Status, strings.TrimSpace(string(b)))
	}
	if s := resp.Header.Get("Mcp-Session-Id"); s != "" {
		c.mu.Lock()
		c.session = s
		c.mu.Unlock()
	}
	return resp, nil
}

func (c *httpConn) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	c.next++
	id := json.RawMessage(fmt.Sprint(c.next))
	c.mu.Unlock()
	resp, err := c.post(ctx, message{ID: id, Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		var m message
		if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
			return nil, fmt.Errorf("%s: %w", c.url, err)
		}
		return m.result()
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	var data strings.Builder
	for sc.Scan() {
		line := sc.Text()
		if d, ok := strings.CutPrefix(line, "data:"); ok {
			data.WriteString(strings.TrimPrefix(d, " "))
			continue
		}
		if line != "" || data.Len() == 0 {
			continue
		}
		var m message
		err := json.Unmarshal([]byte(data.String()), &m)
		data.Reset()
		if err == nil && m.Method == "" && string(m.ID) == string(id) {
			return m.result()
		}
	}
	return nil, fmt.Errorf("%s: stream ended without a response", c.url)
}

func (c *httpConn) notify(ctx context.Context, method string) error {
	resp, err := c.post(ctx, message{Method: method})
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

func (c *httpConn) alive() bool { return true }
func (c *httpConn) close()      {}
