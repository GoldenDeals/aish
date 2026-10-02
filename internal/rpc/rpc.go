// Package rpc connects `aish agent` processes running inside the shell to
// the aish proxy that owns the session. One JSON request per connection.
package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/inebotov/aish/internal/session"
)

type Request struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type Response struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// Methods.
const (
	MethodInfo       = "info"
	MethodHistory    = "history"
	MethodAppend     = "append"
	MethodWaitOutput = "wait_output"
	MethodClear      = "clear"
	MethodModel      = "model" // switch the model and its effort for this shell
	MethodFold       = "fold"  // keep an output for Ctrl+O
	MethodFolds      = "folds" // outputs kept since the request started
	MethodMCPList    = "mcp_list"
	MethodMCPCall    = "mcp_call"
	MethodMCPStatus  = "mcp_status"
	MethodResume     = "resume" // switch this shell to another session
)

// Fold is an output hidden from the terminal, shown again with Ctrl+O.
type Fold struct {
	Title string `json:"title"`
	Text  string `json:"text"`
}

type Info struct {
	SessionID string `json:"session_id"`
	// Dir holds the session's files: the proxy's sessions_dir, which the
	// config a later command reads need not match.
	Dir string `json:"dir"`
	// Model and Effort are what this shell uses, which `aish model` may
	// have changed; Window is the model's context size, 0 if unknown.
	Model  string `json:"model"`
	Effort string `json:"effort,omitempty"`
	Window int    `json:"window"`
}

// ModelParams set both the model and the effort: "" is the model's default.
type ModelParams struct {
	Model  string `json:"model"`
	Effort string `json:"effort,omitempty"`
	Window int    `json:"window"` // 0: the proxy finds out
}

type ResumeParams struct {
	ID string `json:"id"`
}

type AppendParams struct {
	Entries []session.Entry `json:"entries"`
}

type WaitParams struct {
	ID        string `json:"id"`
	TimeoutMS int    `json:"timeout_ms"`
}

// Output is what the proxy captured for one agent-run command.
type Output struct {
	Output string `json:"output"`
	Exit   int    `json:"exit"`
	Cwd    string `json:"cwd"`
	TUI    bool   `json:"tui"`
}

// Handler serves one method call. Ctx is cancelled when the client hangs
// up, so the work for a client that gave up or was killed stops.
type Handler func(ctx context.Context, method string, params json.RawMessage) (any, error)

func Serve(l net.Listener, h Handler) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		go serve(c, h)
	}
}

func serve(c net.Conn, h Handler) {
	defer c.Close()
	var req Request
	if err := json.NewDecoder(c).Decode(&req); err != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The client sends nothing after the request, so a read returns only
	// when it closes the connection.
	go func() {
		io.Copy(io.Discard, c)
		cancel()
	}()
	var resp Response
	res, err := h(ctx, req.Method, req.Params)
	if err != nil {
		resp.Error = err.Error()
	} else {
		resp.Result, _ = json.Marshal(res)
	}
	_ = json.NewEncoder(c).Encode(resp)
}

// Client talks to the proxy at $AISH_SOCK.
type Client struct{ Path string }

func FromEnv() (*Client, error) {
	p := os.Getenv("AISH_SOCK")
	if p == "" {
		return nil, errors.New("AISH_SOCK is not set: run bash under `aish`")
	}
	return &Client{Path: p}, nil
}

const (
	dialTimeout = 2 * time.Second
	// CallTimeout bounds a call made without a deadline of its own: the
	// proxy answers those at once, and a hung one must not hang every aish
	// process in the shell.
	CallTimeout = 10 * time.Second
)

// Call is CallContext with CallTimeout.
func (c *Client) Call(method string, params, result any) error {
	ctx, cancel := context.WithTimeout(context.Background(), CallTimeout)
	defer cancel()
	return c.CallContext(ctx, method, params, result)
}

// CallContext gives up on the call when ctx is done. That closes the
// connection, which cancels the call in the proxy too.
func (c *Client) CallContext(ctx context.Context, method string, params, result any) error {
	d := net.Dialer{Timeout: dialTimeout}
	conn, err := d.DialContext(ctx, "unix", c.Path)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.SetDeadline(time.Now()) })
	defer stop()
	req := Request{Method: method}
	if params != nil {
		req.Params, _ = json.Marshal(params)
	}
	var resp Response
	err = json.NewEncoder(conn).Encode(req)
	if err == nil {
		err = json.NewDecoder(conn).Decode(&resp)
	}
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("no answer from the aish proxy to %s: %w", method, ctx.Err())
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	if resp.Error != "" {
		return errors.New(resp.Error)
	}
	if result != nil && resp.Result != nil {
		return json.Unmarshal(resp.Result, result)
	}
	return nil
}

func (c *Client) History() ([]session.Entry, error) {
	var es []session.Entry
	err := c.Call(MethodHistory, nil, &es)
	return es, err
}

func (c *Client) Append(es ...session.Entry) error {
	return c.Call(MethodAppend, AppendParams{Entries: es}, nil)
}

// WaitOutput waits up to timeout for the output of the agent's command id.
func (c *Client) WaitOutput(id string, timeout time.Duration) (Output, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout+CallTimeout)
	defer cancel()
	var out Output
	err := c.CallContext(ctx, MethodWaitOutput, WaitParams{ID: id, TimeoutMS: int(timeout / time.Millisecond)}, &out)
	return out, err
}
