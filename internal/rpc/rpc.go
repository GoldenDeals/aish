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

	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/policy"
	"github.com/GoldenDeals/aish/internal/session"
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
	MethodYolo    = "yolo"   // turn the checks of the agent's calls off or on for this shell, YoloParams
	MethodFolds   = "folds"  // outputs kept since the request started
	MethodTasks   = "tasks"  // the subagents in the background, or one's output
	MethodResume  = "resume" // switch this shell to another session
	MethodRename  = "rename" // name this shell's session, RenameParams
	// The agent: begin a request, go on after the shell ran a command,
	// start a subagent in the background for the user (SpawnParams), stop
	// the one in progress, sum the session up.
	MethodAgentStart  = "agent_start"
	MethodAgentResume = "agent_resume"
	MethodAgentSpawn  = "agent_spawn"
	MethodAgentCancel = "agent_cancel"
	MethodCompact     = "compact"
	MethodRecap       = "recap" // retell the whole session on the screen
	MethodMCPList     = "mcp_list"
	MethodMCPCall     = "mcp_call"
	MethodMCPStatus   = "mcp_status"
)

// MethodApplyConfig reads the config files anew and puts them in force
// once the user said Yes to the proxy's question: AgentParams with the
// shell's cwd and environment, for the project file and the profile they
// select; Applied back.
const MethodApplyConfig = "apply_config"

// MethodTrust is `aish trust` inside aish: the proxy trusts the project
// file of TrustParams.Cwd, as it is now, once the user said Yes to its
// question; Trusted back.
const MethodTrust = "trust"

// The config in force, for the commands in the shell that show it or go by
// it: MethodConfig is the config of a request from a directory,
// ConfigParams and Config back; MethodModels lists the models of a profile
// of it with its key, which stays in the proxy, ModelsParams and
// []llm.ModelInfo back; MethodPolicy asks its policies about a tool call,
// PolicyParams and policy.Decision back.
const (
	MethodConfig = "config"
	MethodModels = "models"
	MethodPolicy = "policy"
)

// Fold is an output hidden from the terminal, shown again with Ctrl+O.
type Fold struct {
	Title string `json:"title"`
	Text  string `json:"text"`
}

// TasksParams ask for the output of the subagent in the background ID;
// without it, for the list of them.
type TasksParams struct {
	ID string `json:"id,omitempty"`
}

// Task is a subagent in the background: its id (bg1), the subagent, its
// state (queued, running, ok, error, cancelled) and the task it was
// given. Output, what it has shown so far as plain text, comes only for
// the one asked for.
type Task struct {
	ID     string `json:"id"`
	Agent  string `json:"agent"`
	State  string `json:"state"`
	Prompt string `json:"prompt,omitempty"`
	Output string `json:"output,omitempty"`
}

type Info struct {
	SessionID string `json:"session_id"`
	// Dir holds the session's files: the proxy's sessions_dir, which the
	// config a later command reads need not match.
	Dir string `json:"dir"`
	// Saved is whether the session is on disk: it is from its first entry.
	Saved bool `json:"saved"`
	// Name is the one the user gave the session, which the proxy keeps
	// for a session not on disk yet.
	Name string `json:"name,omitempty"`
	// Profile, Model and Effort are what this shell uses, which `aish
	// model` may have changed; Window is the model's context size, 0 if
	// unknown. Profile "" is the top level of config.toml alone.
	Profile string `json:"profile,omitempty"`
	Model   string `json:"model"`
	Effort  string `json:"effort,omitempty"`
	Window  int    `json:"window"`
	// Yolo is whether `aish yolo` has the checks of the agent's calls off.
	Yolo bool `json:"yolo,omitempty"`
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

// YoloParams turn aish yolo on or off: while it is on, the agent's calls
// and its subagents' go by the guard alone, till the shell exits.
type YoloParams struct {
	On bool `json:"on"`
}

// Applied is what `aish apply-config` put in force: Keys of config.toml
// whose values changed (profiles.work.model), Files read anew that changed
// (a project file, a directory of policies, the MCP config), and Info, the
// shell's profile, model and effort, which may have followed the edit.
// Restart names the keys changed since aish started that only a restart
// applies: the proxy takes them as it starts the shell.
type Applied struct {
	Keys    []string `json:"keys,omitempty"`
	Files   []string `json:"files,omitempty"`
	Restart []string `json:"restart,omitempty"`
	Info    Info     `json:"info"`
	// Switched is whether Info differs from what the shell had before.
	Switched bool `json:"switched,omitempty"`
}

// TrustParams name the directory of `aish trust`, the client's: its
// project file is the one trusted.
type TrustParams struct {
	Cwd string `json:"cwd"`
}

// Trusted is the project file `aish trust` trusted and its keys that run
// code from the repository, as config.CodeKeys has them.
type Trusted struct {
	Path string   `json:"path"`
	Keys []string `json:"keys,omitempty"`
}

// ConfigParams ask for the config a request of the shell from Cwd would go
// by; Env is the shell's environment, for the profile config.toml selects
// there. Profile, if set, asks for that profile of config.toml, "" its top
// level, instead of the shell's; Policies, for the policies in force too.
type ConfigParams struct {
	Cwd      string   `json:"cwd"`
	Env      []string `json:"env,omitempty"`
	Profile  *string  `json:"profile,omitempty"`
	Policies bool     `json:"policies,omitempty"`
}

// Config is the config in force for a command in the shell: the files as
// aish read them at its start or at the last `aish apply-config`, not as
// they are on disk.
type Config struct {
	// Config is the profile asked for with the project file of Cwd laid
	// over; its model and effort are config.toml's, the shell's are in
	// Info. The API keys and the proxies of the requests are not in it:
	// they stay in the proxy. The profiles are there by name only.
	Config  config.Config `json:"config"`
	Project string        `json:"project,omitempty"`
	// Global is how many of the [policy] rules of Config are config.toml's;
	// the rest are the project file's.
	Global int `json:"global"`
	// Default is the profile config.toml selects for Env; DefaultErr says
	// why it selects none.
	Default    string `json:"default,omitempty"`
	DefaultErr string `json:"default_err,omitempty"`
	// Policies are the files of the Cedar policies in force, if asked for;
	// PolicyErr says why there are none, and why a request would fail.
	Policies  []policy.Summary `json:"policies,omitempty"`
	PolicyErr string           `json:"policy_err,omitempty"`
	// Changed names the config files on disk that differ from those in
	// force: what `aish apply-config` would apply.
	Changed []string `json:"changed,omitempty"`
}

// ModelsParams ask for the models of Profile, "" the top level of
// config.toml; Env is the shell's environment, for the key.
type ModelsParams struct {
	Profile string   `json:"profile,omitempty"`
	Env     []string `json:"env,omitempty"`
}

// PolicyParams ask the policies in force what they say of a call of Tool
// with Args by the agent of the shell at Cwd with Env: as subagent Agent,
// if set. Line, if HandOff, is the command the call hands the shell;
// Server is the MCP server of the tool, if any.
type PolicyParams struct {
	Cwd     string         `json:"cwd"`
	Env     []string       `json:"env,omitempty"`
	Tool    string         `json:"tool"`
	Args    map[string]any `json:"args,omitempty"`
	Line    string         `json:"line,omitempty"`
	HandOff bool           `json:"hand_off,omitempty"`
	Server  string         `json:"server,omitempty"`
	Agent   string         `json:"agent,omitempty"`
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
	// Overhead is the system prompt and the tool schemas of the last
	// request, in bytes: what the journal does not hold of a context
	// (session.Tokens), 0 before the first request.
	Overhead int `json:"overhead,omitempty"`
	// Since the last compact: the user's commands and requests.
	Commands int `json:"commands"`
	Requests int `json:"requests"`
	// Over the whole journal; the tokens are of the host's turns.
	ToolCalls    int `json:"tool_calls"`
	Compacts     int `json:"compacts"`
	InputTokens  int `json:"input_tokens"`
	CachedTokens int `json:"cached_tokens"`
	OutputTokens int `json:"output_tokens"`
	// Over the whole journal, what the turns of subagents cost
	// (session.KindUsage): the session's spend, no part of its context.
	SubInputTokens  int `json:"sub_input_tokens,omitempty"`
	SubCachedTokens int `json:"sub_cached_tokens,omitempty"`
	SubOutputTokens int `json:"sub_output_tokens,omitempty"`
}

type ResumeParams struct {
	ID string `json:"id"`
}

// ClearParams start this shell's session over: the current one stays on
// disk as it is, and the next one is named Name if given.
type ClearParams struct {
	Name string `json:"name,omitempty"`
}

// RenameParams give this shell's session the user's name, "" none.
type RenameParams struct {
	Name string `json:"name"`
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

// SpawnParams start subagent Agent in the background on the user's Text:
// `&NAME text` at the prompt. Task comes back.
type SpawnParams struct {
	AgentParams
	Agent string `json:"agent"`
}

// Output is what the proxy captured for one agent-run command.
type Output struct {
	Output string `json:"output"`
	Exit   int    `json:"exit"`
	Cwd    string `json:"cwd"`
	TUI    bool   `json:"tui"`
	Why    string `json:"why,omitempty"` // why the proxy had the shell stop it (Esc), "" if it ran to its end
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
	ctx, cancel := context.WithCancel(peerContext(c))
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
