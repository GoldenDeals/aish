// Package rpc connects the aish commands running inside the shell to the
// proxy that owns the session and runs the agent. One JSON request per
// connection; a request the proxy works on for long (agent_start) keeps
// its connection open, and closing it cancels the work.
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
	MethodInfo    = "info"
	MethodStatus  = "status" // Info plus what `aish status` counts
	MethodHistory = "history"
	MethodClear   = "clear"
	MethodModel   = "model"  // switch the model and its effort for this shell
	MethodFolds   = "folds"  // outputs kept since the request started
	MethodResume  = "resume" // switch this shell to another session
	// The agent: begin a request, go on after the shell ran a command,
	// stop the one in progress, sum the session up.
	MethodAgentStart  = "agent_start"
	MethodAgentResume = "agent_resume"
	MethodAgentCancel = "agent_cancel"
	MethodCompact     = "compact"
	MethodMCPList     = "mcp_list"
	MethodMCPCall     = "mcp_call"
	MethodMCPStatus   = "mcp_status"
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
	// Saved is whether the session is on disk.
	Saved bool `json:"saved"`
	// Profile, Model and Effort are what this shell uses, which `aish
	// model` may have changed; Window is the model's context size, 0 if
	// unknown. Profile "" is the top level of config.toml alone.
	Profile string `json:"profile,omitempty"`
	Model   string `json:"model"`
	Effort  string `json:"effort,omitempty"`
	Window  int    `json:"window"`
	// Asking is whether a request of the agent is in progress: what only
	// the user may do is refused.
	Asking bool `json:"asking,omitempty"`
}

// ModelParams set the profile, the model and the effort at once: effort ""
// is the model's default, profile "" the top level of config.toml.
type ModelParams struct {
	Profile string `json:"profile,omitempty"`
	Model   string `json:"model"`
	Effort  string `json:"effort,omitempty"`
	Window  int    `json:"window"` // 0: the proxy finds out
}

// Status is what `aish status` shows of the session, counted by the proxy,
// which has the journal, instead of sending the journal over.
type Status struct {
	Info
	// ProjectConfig is the .aish.toml the last request took, if any.
	ProjectConfig string `json:"project_config,omitempty"`
	// Tokens is the size of the context the next request would send;
	// Measured when the API reported it, else estimated.
	Tokens   int  `json:"tokens"`
	Measured bool `json:"measured"`
	// Since the last compact: the user's commands and requests.
	Commands int `json:"commands"`
	Requests int `json:"requests"`
	// Over the whole journal.
	ToolCalls    int `json:"tool_calls"`
	Compacts     int `json:"compacts"`
	InputTokens  int `json:"input_tokens"`
	CachedTokens int `json:"cached_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type ResumeParams struct {
	ID string `json:"id"`
}

// ClearParams start this shell's session over. Save puts the current
// session on disk, if it is not there yet, named Name if given; otherwise
// an unsaved one is simply dropped. SaveNew makes the next session a saved
// one from its first entry, named NewName if given.
type ClearParams struct {
	Save    bool   `json:"save,omitempty"`
	Name    string `json:"name,omitempty"`
	SaveNew bool   `json:"save_new,omitempty"`
	NewName string `json:"new_name,omitempty"`
}

// AgentParams carry a request to the agent. Cwd and Env are the shell's,
// where the command that sends them runs: the agent lives in the proxy,
// whose own are not the user's.
type AgentParams struct {
	Text string   `json:"text,omitempty"` // agent_start: the request; compact: what to focus on
	ID   string   `json:"id,omitempty"`   // agent_resume: the bash tool call the shell ran
	RC   int      `json:"rc,omitempty"`   // agent_resume: its exit status
	Cwd  string   `json:"cwd"`
	Env  []string `json:"env,omitempty"`
}

// Output is what the proxy captured for one agent-run command.
type Output struct {
	Output string `json:"output"`
	Exit   int    `json:"exit"`
	Cwd    string `json:"cwd"`
	TUI    bool   `json:"tui"`
}

// Handler serves one method call. Ctx is cancelled when the client hangs
// up, so the work for a client that gave up or was killed stops: `aish
// agent` holds its connection for as long as the proxy works on its
// request.
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
// connection, which cancels the call in the proxy too. A call that may
// take long, such as agent_start, is made with a context of its own.
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
