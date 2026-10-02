// Package rpc connects `aish agent` processes running inside the shell to
// the aish proxy that owns the session. One JSON request per connection.
package rpc

import (
	"encoding/json"
	"errors"
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
	MethodModel      = "model" // switch the model for this shell
	MethodFold       = "fold"  // keep an output for Ctrl+O
	MethodFolds      = "folds" // outputs kept since the request started
	MethodMCPList    = "mcp_list"
	MethodMCPCall    = "mcp_call"
)

// Fold is an output hidden from the terminal, shown again with Ctrl+O.
type Fold struct {
	Title string `json:"title"`
	Text  string `json:"text"`
}

type Info struct {
	SessionID string `json:"session_id"`
	// Model is the one this shell uses, which `aish model` may have changed;
	// Window is its context size, 0 if unknown.
	Model  string `json:"model"`
	Window int    `json:"window"`
}

type ModelParams struct {
	Model  string `json:"model"`
	Window int    `json:"window"` // 0: the proxy finds out
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

// Handler serves one method call.
type Handler func(method string, params json.RawMessage) (any, error)

func Serve(l net.Listener, h Handler) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			var req Request
			if err := json.NewDecoder(c).Decode(&req); err != nil {
				return
			}
			var resp Response
			res, err := h(req.Method, req.Params)
			if err != nil {
				resp.Error = err.Error()
			} else {
				resp.Result, _ = json.Marshal(res)
			}
			_ = json.NewEncoder(c).Encode(resp)
		}()
	}
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

func (c *Client) Call(method string, params, result any) error {
	conn, err := net.DialTimeout("unix", c.Path, 2*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	req := Request{Method: method}
	if params != nil {
		req.Params, _ = json.Marshal(params)
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return err
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
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

func (c *Client) WaitOutput(id string, timeout time.Duration) (Output, error) {
	var out Output
	err := c.Call(MethodWaitOutput, WaitParams{ID: id, TimeoutMS: int(timeout / time.Millisecond)}, &out)
	return out, err
}
