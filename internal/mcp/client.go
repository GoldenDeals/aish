// Package mcp makes the tools of MCP servers ordinary aish tools. The proxy
// owns the server processes for the life of the shell and starts each one
// when it is first needed; the agent reaches them there, and `aish tool
// NAME`, typed by the user or run from bash, over the proxy's socket.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/GoldenDeals/aish/internal/config"
)

const protocolVersion = "2025-06-18"

// cancelTimeout bounds what is said to a server only as a courtesy, with
// nobody waiting for the outcome: the cancellation of a request the caller
// has gone from, the end of a session, the reply to the server's request.
const cancelTimeout = 2 * time.Second

// Server is one entry of mcp.yaml: a command speaking MCP on stdio, or the
// URL of a Streamable HTTP endpoint. Its fields are config.MCPServer's.
type Server struct {
	Command string            `yaml:"command" json:"command,omitempty"`
	Args    []string          `yaml:"args" json:"args,omitempty"`
	Env     map[string]string `yaml:"env" json:"env,omitempty"`
	URL     string            `yaml:"url" json:"url,omitempty"`
	Headers map[string]string `yaml:"headers" json:"headers,omitempty"`
	// EnvCommand and HeadersCommand give a variable or a header the output
	// of a command instead of a literal: a token kept in pass, say. A key
	// is in one of the two maps, not in both (resolve).
	EnvCommand     map[string]string `yaml:"env_command" json:"env_command,omitempty"`
	HeadersCommand map[string]string `yaml:"headers_command" json:"headers_command,omitempty"`
	// Expose is "deferred" (the default), for tools the model sees by name
	// and loads with tool_search, or "tools", whose schemas it is given with
	// every request.
	Expose string `yaml:"expose" json:"expose,omitempty"`
	// Timeout of a tool call, in seconds.
	Timeout int `yaml:"timeout" json:"timeout,omitempty"`
}

// FromConfig is the servers of the MCP config, as config.Snapshot.Servers
// read them, as the manager runs them. Server stays a type of its own: the
// key of a server's cache is its JSON (NewManager).
func FromConfig(cfgs map[string]config.MCPServer) map[string]Server {
	if cfgs == nil {
		return nil
	}
	servers := make(map[string]Server, len(cfgs))
	for name, c := range cfgs {
		servers[name] = Server(c)
	}
	return servers
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
	notify(ctx context.Context, method string, params any) error
	close()
	alive() bool
}

// dial starts the server of s, or opens a session with it: the one place
// where the values resolve gives become the server's environment and
// headers. secrets are those of them maskError cannot find in s: the
// output of the commands, whatever the key, and the literals as expanded.
func dial(ctx context.Context, s Server) (c conn, secrets []string, err error) {
	env, headers, err := resolve(ctx, s)
	if err != nil {
		return nil, nil, err
	}
	for k, v := range env {
		if _, ok := s.EnvCommand[k]; ok || secretWord.MatchString(k) {
			secrets = append(secrets, v)
		}
	}
	for _, v := range headers {
		secrets = append(secrets, v)
	}
	if s.URL != "" {
		return &httpConn{url: s.URL, headers: headers}, secrets, nil
	}
	sc, err := startStdio(s, env)
	if err != nil {
		return nil, secrets, err
	}
	return sc, secrets, nil
}

// initialize performs the MCP handshake and returns the instructions the
// server gives for the use of its tools, "" for none. Instructions that are
// not a string count as none: they are a hint, not worth the server.
func initialize(ctx context.Context, c conn) (instructions string, err error) {
	raw, err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "aish", "version": "0.1"},
	})
	if err != nil {
		return "", fmt.Errorf("initialize: %w", err)
	}
	var res struct {
		Instructions json.RawMessage `json:"instructions"`
	}
	if json.Unmarshal(raw, &res) == nil && len(res.Instructions) > 0 {
		json.Unmarshal(res.Instructions, &instructions)
	}
	return strings.TrimSpace(instructions), c.notify(ctx, "notifications/initialized", nil)
}

// cancelRequest tells the server in the background that nobody waits for
// request id anymore, so that it can stop the work. The server may never
// hear it; the error is of no use to anyone.
func cancelRequest(c conn, method string, id json.RawMessage, why error) {
	if method == "initialize" {
		return // the spec forbids cancelling it
	}
	reason := "cancelled by the user"
	if errors.Is(why, context.DeadlineExceeded) {
		reason = "timed out"
	}
	go func() {
		ctx, stop := context.WithTimeout(context.Background(), cancelTimeout)
		defer stop()
		c.notify(ctx, "notifications/cancelled", map[string]any{"requestId": id, "reason": reason})
	}()
}

type stdio struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	stderr *tail

	// wmu orders the writes to the server's stdin. Not mu: a server that
	// stops reading blocks a write for good.
	wmu sync.Mutex

	mu      sync.Mutex
	next    int
	pending map[string]chan message
	dead    chan struct{}
	err     error
	closed  bool // given up on, though the process may still be exiting
	// reaped is set once Wait returns: the process's pid, which is also the
	// id of its group, may be another process's after that.
	reaped bool
	// orphan is set while wmu is held by a notification or a reply given up
	// on, its line still not taken by the server: nobody waits for it to
	// give the server up.
	orphan bool

	stop sync.Once
}

// startStdio runs the server of s with env, as resolve gave it, over the
// proxy's environment.
func startStdio(s Server, env map[string]string) (*stdio, error) {
	cmd := exec.Command(s.Command, s.Args...)
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
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
			// Not in this goroutine: a server that does not read would stop
			// the reading too, and with it the answers to the calls that wait.
			go func() {
				ctx, stop := context.WithTimeout(context.Background(), cancelTimeout)
				defer stop()
				c.write(ctx, reply, false)
			}()
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
	c.reaped = true
	c.err = fmt.Errorf("server exited (%v)%s", err, c.stderr.String())
	c.mu.Unlock()
	close(c.dead)
}

// write sends m unless ctx ends first. A call whose line the server does
// not take is given up with the server: the line it got may be cut short,
// and nothing can be said to it after that. A notification or a reply to
// the server is not worth the server, which may be busy rather than deaf:
// its line is left to go through when the server reads again, and only a
// call stuck behind it gives the server up.
func (c *stdio) write(ctx context.Context, m message, call bool) error {
	m.JSONRPC = "2.0"
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	var (
		mu                        sync.Mutex
		started, gaveUp, finished bool
	)
	done := make(chan error, 1)
	go func() {
		c.wmu.Lock()
		defer c.wmu.Unlock()
		mu.Lock()
		started = !gaveUp
		mu.Unlock()
		if !started {
			return
		}
		_, err := c.in.Write(append(b, '\n'))
		mu.Lock()
		finished = true
		if gaveUp {
			c.setOrphan(false)
		}
		mu.Unlock()
		done <- err
	}()
	wrote := func(err error) error {
		if err != nil {
			return c.failure(err)
		}
		return nil
	}
	var stop error
	select {
	case err := <-done:
		return wrote(err)
	case <-c.dead:
		stop = c.failure(nil)
	case <-ctx.Done():
		stop = ctx.Err()
	}
	mu.Lock()
	gaveUp = true
	hung := started && !finished
	if hung && !call {
		c.setOrphan(true)
	}
	mu.Unlock()
	switch {
	case started && !hung:
		return wrote(<-done) // it got through after all
	case call && (hung || c.orphaned()):
		// Not when it waited for another call's line: that call gives the
		// server up itself if it has to.
		c.abandon()
	}
	return stop
}

func (c *stdio) setOrphan(v bool) {
	c.mu.Lock()
	c.orphan = v
	c.mu.Unlock()
}

func (c *stdio) orphaned() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.orphan
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
	if err := c.write(ctx, message{ID: id, Method: method, Params: params}, true); err != nil {
		return nil, err
	}
	select {
	case m := <-ch:
		return m.result()
	case <-c.dead:
		return nil, c.failure(nil)
	case <-ctx.Done():
		cancelRequest(c, method, id, ctx.Err())
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

func (c *stdio) notify(ctx context.Context, method string, params any) error {
	return c.write(ctx, message{Method: method, Params: params}, false)
}

func (c *stdio) alive() bool {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return false
	}
	select {
	case <-c.dead:
		return false
	default:
		return true
	}
}

// abandon gives the server up at once, so that the next call starts a new
// one, and stops it in the background.
func (c *stdio) abandon() {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	go c.close()
}

func (c *stdio) close() {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	// Once: a server given up on is closed again when it is replaced, and
	// its process group may be another one's by then.
	c.stop.Do(func() {
		// Also wakes up a write blocked on the pipe.
		c.in.Close()
		if c.signal(syscall.SIGTERM) {
			select {
			case <-c.dead:
			case <-time.After(time.Second):
				c.signal(syscall.SIGKILL)
			}
		}
	})
}

// signal sends sig to the server's process group unless the server is
// reaped. Children it left behind are not signalled then: the group's id
// is no longer known to be theirs.
func (c *stdio) signal(sig syscall.Signal) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.reaped || c.cmd.Process == nil {
		return false
	}
	syscall.Kill(-c.cmd.Process.Pid, sig)
	return true
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
	expired bool // the server forgot the session; a new one needs initialize
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
	session := c.session
	c.mu.Unlock()
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound && session != "" {
		// The answer to a session the server no longer knows: it restarted,
		// or the session expired.
		resp.Body.Close()
		c.mu.Lock()
		c.expired = true
		c.mu.Unlock()
		return nil, fmt.Errorf("%s: session expired", c.url)
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

func (c *httpConn) call(ctx context.Context, method string, params any) (_ json.RawMessage, err error) {
	c.mu.Lock()
	c.next++
	id := json.RawMessage(fmt.Sprint(c.next))
	c.mu.Unlock()
	defer func() {
		if err != nil && ctx.Err() != nil {
			// Whether the request reached the server is unknown; a server
			// ignores the cancellation of a request it never saw.
			cancelRequest(c, method, id, ctx.Err())
			err = ctx.Err()
		}
	}()
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

func (c *httpConn) notify(ctx context.Context, method string, params any) error {
	resp, err := c.post(ctx, message{Method: method, Params: params})
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

func (c *httpConn) alive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.expired
}

// close ends the session at the server, which would keep it until it
// expires otherwise. Even one the server answered 404 to: another instance
// behind the URL may keep it. Whatever the answer, nothing more is to be
// done.
func (c *httpConn) close() {
	c.mu.Lock()
	session := c.session
	c.session = ""
	c.mu.Unlock()
	if session == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), cancelTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.url, nil)
	if err != nil {
		return
	}
	req.Header.Set("MCP-Protocol-Version", protocolVersion)
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Mcp-Session-Id", session)
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
	}
}
