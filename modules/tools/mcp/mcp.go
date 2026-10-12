// Package mcp gives the model the tools of MCP servers. On Start it connects to each server from the config,
// in the background, and declares its tools as mcp__<server>__<tool>; tools come only from servers that connect,
// one that doesn't is skipped with a warning in the log. Only tools: resources and prompts are not used.
// A value in env or headers can come from a command, a secret store: the first line it prints, read on Init.
//
//	tools.mcp:
//	  servers:
//	    fs:                               # local: a command that speaks MCP on stdin and stdout
//	      command: npx
//	      args: [-y, "@modelcontextprotocol/server-filesystem", "."]
//	      env: {LOG_LEVEL: error}         # added to aish's environment
//	    exa:                              # remote, over streamable HTTP; HTTPS_PROXY applies
//	      url: https://mcp.exa.ai/mcp
//	      headers: {x-api-key: {command: pass show exa/mcp/token}} # run by sh
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GoldenDeals/aish-core/contracts/llm"
	"github.com/GoldenDeals/aish-core/contracts/tool"
	"github.com/GoldenDeals/aish-core/modules/tools/internal/serve"
	"github.com/GoldenDeals/aish-core/pkg/bus"
	"github.com/GoldenDeals/aish-core/pkg/config"
	"github.com/GoldenDeals/aish-core/pkg/event"
	"github.com/GoldenDeals/aish-core/pkg/module"
)

// invalid is what tool names can't have: the APIs take [a-zA-Z0-9_-], up to 64 of them.
var invalid = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

type server struct {
	name      string
	transport sdk.Transport
}

// target is where a declared tool goes: its server and its name there.
type target struct {
	session *sdk.ClientSession
	name    string
}

type mod struct {
	b       bus.Bus
	client  *sdk.Client
	servers []server
	ctx     context.Context // ends on Stop, stopping connections and calls in flight
	cancel  context.CancelFunc

	mu       sync.Mutex
	sessions []*sdk.ClientSession // to close on Stop
	tools    map[string]target
}

func init() { module.Register("tools.mcp", &mod{}) }

func (m *mod) Init(b bus.Bus, cfg config.Config) error {
	m.b, m.tools = b, map[string]target{}
	m.client = sdk.NewClient(&sdk.Implementation{Name: "aish", Version: "v0"}, nil)
	m.ctx, m.cancel = context.WithCancel(context.Background())
	list, ok := cfg.Map()["servers"].(map[string]any)
	if v := cfg.Map()["servers"]; v != nil && !ok {
		return errors.New("servers: want a map of server names to their settings")
	}
	for name, v := range list {
		t, err := transport(v)
		if err == nil && invalid.MatchString(name) {
			err = errors.New("want only letters, digits, _ and -")
		}
		if err != nil {
			return fmt.Errorf("server %q: %w", name, err)
		}
		m.servers = append(m.servers, server{name, t})
	}
	bus.Async(b, func(ev event.Any) bool {
		u, ok := ev.(event.Event[tool.Use])
		if !ok || u.Name() != tool.Call {
			return false
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		_, ok = m.tools[u.Data().Name]
		return ok
	}, m.call)
	return nil
}

// transport makes the transport of a server from its settings.
func transport(v any) (sdk.Transport, error) {
	s, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("want a map with command or url")
	}
	command, _ := s["command"].(string)
	url, _ := s["url"].(string)
	if (command == "") == (url == "") {
		return nil, errors.New("set one of command and url")
	}
	if url != "" {
		h, err := dict(s, "headers")
		return &sdk.StreamableClientTransport{Endpoint: url, HTTPClient: &http.Client{Transport: headers(h)}}, err
	}
	args, ok := s["args"].([]any)
	if s["args"] != nil && !ok {
		return nil, errors.New("args: want a list")
	}
	env, err := dict(s, "env")
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(command) // its stderr goes nowhere: it would break the terminal UI
	for _, a := range args {
		cmd.Args = append(cmd.Args, fmt.Sprint(a))
	}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	if cmd.Env != nil {
		cmd.Env = append(os.Environ(), cmd.Env...)
	}
	return &sdk.CommandTransport{Command: cmd}, nil
}

// dict reads s[key], a map of scalars, as strings; a value {command: ...} is the first line the command prints.
func dict(s map[string]any, key string) (map[string]string, error) {
	v, ok := s[key].(map[string]any)
	if s[key] != nil && !ok {
		return nil, fmt.Errorf("%s: want a map", key)
	}
	out := map[string]string{}
	for k, x := range v {
		c, ok := x.(map[string]any)
		if !ok {
			out[k] = fmt.Sprint(x)
			continue
		}
		command, _ := c["command"].(string)
		b, err := exec.Command("sh", "-c", command).Output()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			err = fmt.Errorf("%w: %s", err, bytes.TrimSpace(exit.Stderr))
		}
		line, _, _ := strings.Cut(string(b), "\n")
		if err == nil && line == "" {
			err = errors.New("printed nothing")
		}
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %s: %w", key, k, command, err)
		}
		out[k] = line
	}
	return out, nil
}

// headers adds itself to every request of a remote server.
type headers map[string]string

func (h headers) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range h {
		r.Header.Set(k, v)
	}
	return http.DefaultTransport.RoundTrip(r)
}

func (m *mod) Start(context.Context) error {
	for _, s := range m.servers {
		go m.connect(s) // npx may take a while to fetch a server: don't hold the others up
	}
	return nil
}

func (m *mod) Stop(context.Context) error {
	m.cancel()
	m.mu.Lock()
	defer m.mu.Unlock()
	var errs []error
	for _, s := range m.sessions {
		errs = append(errs, s.Close())
	}
	m.sessions = nil
	return errors.Join(errs...)
}

func (m *mod) connect(s server) {
	cs, err := m.client.Connect(m.ctx, s.transport, nil)
	if err != nil {
		slog.Warn("mcp connect", "module", "tools.mcp", "server", s.name, "err", err)
		return
	}
	m.mu.Lock()
	if m.ctx.Err() != nil { // stopped while connecting
		m.mu.Unlock()
		cs.Close()
		return
	}
	m.sessions = append(m.sessions, cs)
	m.mu.Unlock()

	n := 0
	// shortcut: the tools are listed once; follow notifications/tools/list_changed if servers change them on the fly
	for t, err := range cs.Tools(m.ctx, nil) {
		if err != nil {
			slog.Warn("mcp tools", "module", "tools.mcp", "server", s.name, "err", err)
			break
		}
		name := invalid.ReplaceAllString("mcp__"+s.name+"__"+t.Name, "_")
		name = name[:min(len(name), 64)]
		schema, _ := json.Marshal(t.InputSchema)
		m.mu.Lock()
		_, dup := m.tools[name]
		if !dup {
			m.tools[name] = target{cs, t.Name}
		}
		m.mu.Unlock()
		if dup {
			slog.Warn("mcp tool name taken", "module", "tools.mcp", "server", s.name, "tool", t.Name, "name", name)
			continue
		}
		if err := m.b.Dispatch(m.ctx, event.New(tool.Declare, tool.Spec{Name: name, Description: t.Description, Schema: schema})); err != nil {
			slog.Warn("mcp declare", "module", "tools.mcp", "server", s.name, "tool", t.Name, "err", err)
			continue
		}
		n++
	}
	slog.Info("mcp connected", "module", "tools.mcp", "server", s.name, "tools", n)
}

func (m *mod) call(actx context.Context, ev event.Event[tool.Use]) {
	u := ev.Data()
	m.mu.Lock()
	t := m.tools[u.Name]
	m.mu.Unlock()
	slog.Info("tool call", "module", "tools.mcp", "tool", u.Name, "id", u.ID)
	slog.Debug("tool call", "module", "tools.mcp", "tool", u.Name, "id", u.ID, "input", string(u.Input))
	ctx, cancel := context.WithCancel(m.ctx)
	defer cancel()
	defer context.AfterFunc(llm.Turn(actx), cancel)() // the user stopped the turn: the SDK tells the server

	o := tool.Output{ID: u.ID}
	res, err := t.session.CallTool(ctx, &sdk.CallToolParams{Name: t.name, Arguments: u.Input})
	if err != nil {
		o.Text, o.Error = err.Error(), true
	} else {
		o.Text, o.Error = text(res), res.IsError
	}
	if o.Error {
		slog.Warn("tool error", "module", "tools.mcp", "tool", u.Name, "id", u.ID, "err", o.Text)
	}
	o.Text = serve.Cut(o.Text)
	slog.Debug("tool result", "module", "tools.mcp", "tool", u.Name, "id", u.ID, "text", o.Text)
	_ = m.b.Dispatch(actx, event.New(tool.Result, o)) // a failed tx has nowhere to report to
}

// text is what the model gets of a result: its text, else its structured content.
func text(r *sdk.CallToolResult) string {
	var parts []string
	for _, c := range r.Content {
		if t, ok := c.(*sdk.TextContent); ok {
			parts = append(parts, t.Text)
		} else {
			// shortcut: tool.Output is text only; pass images and resources once it can carry them
			parts = append(parts, fmt.Sprintf("[%T left out]", c))
		}
	}
	if len(parts) == 0 && r.StructuredContent != nil {
		b, _ := json.Marshal(r.StructuredContent)
		return string(b)
	}
	return strings.Join(parts, "\n")
}
