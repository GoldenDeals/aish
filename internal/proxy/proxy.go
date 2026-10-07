// Package proxy runs the user's bash inside a pseudo-terminal, passes all
// bytes through untouched except aish markers, and records command output
// into the session.
package proxy

import (
	"context"
	"io"
	"sync"

	"github.com/GoldenDeals/aish/internal/agent"
	"github.com/GoldenDeals/aish/internal/bashstate"
	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/mcp"
	"github.com/GoldenDeals/aish/internal/policy"
	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/session"
)

// Proxy owns the session, the output recorder and the agent.
type Proxy struct {
	sess         *session.Session
	foldLines    int // the request's fold_lines, the project's included; before one, config.toml's
	maxOutput    int
	overhead     int // the agent's Overhead after the last request, under p.mu
	promptStatus bool
	compactAt    float64      // compact_at: the status says when the next request compacts
	ignore       []string     // journal_ignore: commands recorded without their output
	stateIgnore  []string     // state_ignore: variables kept out of the shell state
	fixedWindow  bool         // context_window is set in the config
	prov         llm.Provider // for the models list and the levels of effort; nil if unknown
	out          io.Writer    // the terminal
	size         func() (w, h int)

	mu      sync.Mutex
	screen  Screen
	asking  bool                // inside __aish_ask, between ask-start and the next prompt
	handed  string              // call id of the agent's command left for the shell, until its output is taken
	user    *segment            // command typed by the user, between cmd-start and cmd-end
	agent   map[string]*segment // commands run on behalf of the agent, by call id
	tool    *fold               // live output of an external tool, while it runs
	at      *statusAt           // where the agent left the cursor after printing its next command
	hide    bool                // the agent's next command is not to be drawn (hide_work), see ui.HideCommand
	waits   bool                // and its line of calls is left open for that command, until the agent writes
	spin    *spin               // that line, kept turning while the command runs, see hidework.go
	watch   *promptWatch        // the fold of another command, watched for a prompt, see foldprompt.go
	line    *inputLine          // the line typed at the prompt, kept off its status
	col     firstCol            // whether the shell's output left the next prompt off the first column
	folds   []Fold              // folded outputs of the last request, for Ctrl+O
	view    *viewer             // open while Ctrl+O shows the folds
	panes   *panes              // open while subagents run, see panes.go
	held    []byte              // shell output that arrived while the viewer was open
	ask     *prompt             // a question the agent waits for the user to answer
	form    *openForm           // the questions of ask_user while the user answers them
	early   *early              // keys typed before readline has the terminal, see early.go
	seq     keySeq              // keys a read cut, and pastes, see pastebrackets.go
	done    map[string]rpc.Output
	waiters map[string]chan struct{}
	mcp     *mcp.Manager
	model   string // `aish model` switches it for this shell
	effort  string // and this, "" being the model's default
	window  int    // its context size, 0 if unknown
	profile string // and the profile of config.toml they are of, "" for its top level
	// defProfile is the one config.toml selects, as the last request (or
	// the start) read it, which the status does not name.
	defProfile  string
	defErr      string // why config.toml selects none, as tellDefErr told it last
	shellConfig string // the shell's other $AISH_CONFIG, as tellConfigPath told of it last
	windowAsked string // what lookupOnce last asked about, the key included

	// The shell's state: how it started, how it was at the last prompt, and
	// what of it was saved last (session id and all).
	run       string
	base, cur *bashstate.State
	lastSaved []byte
	switched  bool // `aish resume` switched the session during this command

	restore string // the script that brings back a resumed session
	resumed *session.Saved

	// The agent, see agenthost.go. cancelReq and reqCtx, the context of the
	// request it stops, are under p.mu; the rest is the request's own, one
	// at a time under reqMu.
	reqMu        sync.Mutex
	ag           *agent.Agent
	fg           func() (int, error) // the shell's foreground process group (peer.go), under p.mu; nil before Run, tests set it
	cancelReq    context.CancelFunc
	reqCtx       context.Context
	cancelGen    uint64 // agent_cancel calls so far, under p.mu
	policies     policy.Cache
	project      string // the .aish.toml of the last request, "" if none
	untrusted    map[string]bool
	agentProv    llm.Provider
	agentProvKey string
	newProvider  func(config.Config) (llm.Provider, error) // nil: llm.New; tests set it

	// The config files as read at the start or by `aish apply-config`,
	// under p.mu, see applyconfig.go. started is the config Run got: what
	// only a restart applies is told by it. mcpFile is the MCP config the
	// servers of p.mcp are of, mcpSum its sha256 when read. confSaid are
	// the edits on disk, not applied yet, that tellChanged told of.
	conf     *config.Snapshot
	started  *config.Config
	mcpFile  string
	mcpSum   string
	confSaid map[string]bool
}

func New(sess *session.Session) *Proxy {
	return &Proxy{
		sess:    sess,
		agent:   map[string]*segment{},
		done:    map[string]rpc.Output{},
		waiters: map[string]chan struct{}{},
		mcp:     mcp.NewManager(nil, ""),
	}
}

// session is the current session for code that does not hold p.mu.
func (p *Proxy) session() *session.Session {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sess
}
