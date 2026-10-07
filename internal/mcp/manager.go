package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	startTimeout = 30 * time.Second // npx may download the server first
	callTimeout  = 60 * time.Second
	retryAfter   = time.Minute // a failed server is not started again by listings sooner
)

// ToolInfo is one MCP tool as the proxy reports it.
type ToolInfo struct {
	Name        string          `json:"name"` // <server>_<tool>, the name for `aish tool`
	Server      string          `json:"server"`
	Tool        string          `json:"tool"` // the server's own name
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema,omitempty"`
	Expose      string          `json:"expose,omitempty"`
	// Timeout is how long a call may take in the proxy, the server's start
	// included, and twice that for an HTTP server, where the call may be
	// repeated in a new session after its start: the client waits as long.
	// Set by List, not cached.
	Timeout time.Duration `json:"timeout,omitempty"`
}

type ListParams struct {
	// Wait starts the servers whose tools are not known yet.
	Wait bool `json:"wait"`
}

type ListResult struct {
	Tools  []ToolInfo `json:"tools"`
	Errors []string   `json:"errors,omitempty"`
	// Instructions are what the servers of Tools say about the use of their
	// tools, by server; those with none are not here.
	Instructions map[string]string `json:"instructions,omitempty"`
}

type CallParams struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

// Manager owns the MCP servers of one shell. A server is started on its
// first call; its tool list and instructions are cached on disk, so the
// next shell knows them from its start without starting anything.
type Manager struct {
	servers  []*server
	cacheDir string

	mu    sync.Mutex
	notes map[string]bool // caches not written, and why
}

type server struct {
	name string
	cfg  Server
	key  string // of the config, to invalidate the cache

	mu       sync.Mutex
	conn     conn
	tools    []ToolInfo
	known    bool
	err      error
	failed   time.Time
	starting *startup
	// secrets are the values of the last start that maskError cannot find
	// in cfg: dial's.
	secrets []string
	// instructions are what initialize said about the use of the tools;
	// like tools, known from the cache before the server starts.
	instructions string
}

// startup is a server start in progress; everyone who needs the server
// waits for it.
type startup struct {
	done chan struct{}
	conn conn
	err  error
}

// NewManager makes the manager of the servers of cfgs, as mcp.yaml gives
// them, knowing from the cache in cacheDir what it can without starting
// any: their tools and instructions.
func NewManager(cfgs map[string]Server, cacheDir string) *Manager {
	m := &Manager{cacheDir: cacheDir, notes: map[string]bool{}}
	names := make([]string, 0, len(cfgs))
	for n := range cfgs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		cfg := cfgs[n]
		if cfg.Expose == "" {
			cfg.Expose = "deferred"
		}
		b, _ := json.Marshal(cfg)
		sum := sha256.Sum256(b)
		s := &server{name: n, cfg: cfg, key: hex.EncodeToString(sum[:8])}
		m.readCache(s)
		m.servers = append(m.servers, s)
	}
	return m
}

// cacheFile is what save writes for a server and readCache reads. A file
// written before instructions were cached has none: the server gave none
// as far as the next shell knows, till it starts again.
type cacheFile struct {
	Key          string     `json:"key"`
	Tools        []ToolInfo `json:"tools"`
	Instructions string     `json:"instructions,omitempty"`
}

// readCache gives s, not started yet, the tools and instructions of its
// cache, unless the cache is of another config.
func (m *Manager) readCache(s *server) {
	var c cacheFile
	if b, err := os.ReadFile(m.cachePath(s.name)); err == nil && json.Unmarshal(b, &c) == nil && c.Key == s.key {
		s.tools, s.instructions, s.known = c.Tools, c.Instructions, true
	}
}

func (m *Manager) cachePath(server string) string {
	return filepath.Join(m.cacheDir, server+".json")
}

// Warm starts, in the background, the servers never seen before to learn
// their tools. Not one with env_command or headers_command: its commands
// wait for its first use, lest pass ask for a passphrase as the shell
// starts.
func (m *Manager) Warm() {
	for _, s := range m.servers {
		if !s.known && len(s.cfg.EnvCommand) == 0 && len(s.cfg.HeadersCommand) == 0 {
			go m.ensure(context.Background(), s, false)
		}
	}
}

func (m *Manager) List(ctx context.Context, wait bool) ListResult {
	var res ListResult
	if wait {
		// All at once, so that the client waits startTimeout, not that
		// many times over.
		errs := make([]error, len(m.servers))
		var wg sync.WaitGroup
		for i, s := range m.servers {
			s.mu.Lock()
			known := s.known
			s.mu.Unlock()
			if !known {
				wg.Go(func() { _, errs[i] = m.ensure(ctx, s, false) })
			}
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", m.servers[i].name, err))
			}
		}
	}
	for _, s := range m.servers {
		s.mu.Lock()
		for _, t := range s.tools {
			t.Timeout = startTimeout + s.timeout()
			if s.cfg.URL != "" {
				t.Timeout *= 2 // Call may start a new session and repeat it there
			}
			res.Tools = append(res.Tools, t)
		}
		if len(s.tools) > 0 && s.instructions != "" {
			if res.Instructions == nil {
				res.Instructions = map[string]string{}
			}
			res.Instructions[s.name] = s.instructions
		}
		s.mu.Unlock()
	}
	m.mu.Lock()
	for n := range m.notes {
		res.Errors = append(res.Errors, n)
	}
	m.mu.Unlock()
	sort.Strings(res.Errors)
	return res
}

// Call runs a tool and returns the server's CallToolResult.
func (m *Manager) Call(ctx context.Context, name string, args map[string]any) (json.RawMessage, error) {
	s, t := m.find(name)
	if s == nil {
		m.List(ctx, true)
		if s, t = m.find(name); s == nil {
			return nil, fmt.Errorf("no MCP tool %q", name)
		}
	}
	c, err := m.ensure(ctx, s, true)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", s.name, err)
	}
	if args == nil {
		args = map[string]any{}
	}
	call := func(c conn) (json.RawMessage, error) {
		ctx, cancel := context.WithTimeout(ctx, s.timeout())
		defer cancel()
		return c.call(ctx, "tools/call", map[string]any{"name": t.Tool, "arguments": args})
	}
	res, err := call(c)
	// An HTTP server that forgot the session did not run the call, so it is
	// safe to repeat in a new one. A stdio server that died may have run it.
	if err != nil && s.cfg.URL != "" && !c.alive() {
		if c, err = m.ensure(ctx, s, true); err == nil {
			res, err = call(c)
		}
	}
	if err != nil {
		s.mu.Lock()
		err = s.mask(err) // the end of the server's stderr, say
		s.mu.Unlock()
		return nil, fmt.Errorf("%s: %w", s.name, err)
	}
	return res, nil
}

// mask is err with the secrets of s hidden; err itself if it has none.
// Called under s.mu.
func (s *server) mask(err error) error {
	if msg := maskError(err.Error(), s.cfg, s.secrets); msg != err.Error() {
		return errors.New(msg)
	}
	return err
}

// timeout is that of a tool call.
func (s *server) timeout() time.Duration {
	if s.cfg.Timeout > 0 {
		return time.Duration(s.cfg.Timeout) * time.Second
	}
	return callTimeout
}

func (m *Manager) find(name string) (*server, ToolInfo) {
	for _, s := range m.servers {
		s.mu.Lock()
		for _, t := range s.tools {
			if t.Name == name {
				s.mu.Unlock()
				return s, t
			}
		}
		s.mu.Unlock()
	}
	return nil, ToolInfo{}
}

// ensure returns a live connection to s, starting the server if needed. A
// recent failure is returned as is unless force.
func (m *Manager) ensure(ctx context.Context, s *server, force bool) (conn, error) {
	for {
		s.mu.Lock()
		if s.conn != nil && s.conn.alive() {
			defer s.mu.Unlock()
			return s.conn, nil
		}
		st, mine := s.starting, false
		if st == nil {
			if s.err != nil && !force && time.Since(s.failed) < retryAfter {
				defer s.mu.Unlock()
				return nil, s.err
			}
			if s.conn != nil {
				// What the old one holds, a process or a session at the
				// server, is let go without anyone waiting for it.
				go s.conn.close()
				s.conn = nil
			}
			st, mine = &startup{done: make(chan struct{})}, true
			s.starting = st
			go m.start(s, st)
		}
		s.mu.Unlock()
		select {
		case <-st.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if mine {
			return st.conn, st.err
		}
	}
}

// start does not stop with the caller that asked for it: others may be
// waiting for the server too, and the start is bounded by startTimeout.
func (m *Manager) start(s *server, st *startup) {
	c, tools, instructions, err := s.connect()
	s.mu.Lock()
	s.starting = nil
	if err != nil {
		// Here, not only in Status: the error goes to the agent too,
		// through Call and List, and a server may print its token.
		err = s.mask(err)
		s.err, s.failed = err, time.Now()
	} else {
		s.conn, s.tools, s.instructions, s.known, s.err = c, tools, instructions, true, nil
	}
	s.mu.Unlock()
	if err == nil {
		m.save(s, cacheFile{Key: s.key, Tools: tools, Instructions: instructions})
	}
	st.conn, st.err = c, err
	close(st.done)
}

var unsafeName = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

func (s *server) connect() (_ conn, _ []ToolInfo, instructions string, _ error) {
	ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
	defer cancel()
	c, secrets, err := dial(ctx, s.cfg)
	s.mu.Lock()
	s.secrets = secrets
	s.mu.Unlock()
	if err != nil {
		return nil, nil, "", err
	}
	instructions, err = initialize(ctx, c)
	if err != nil {
		c.close()
		return nil, nil, "", err
	}
	var tools []ToolInfo
	cursor := ""
	for {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := c.call(ctx, "tools/list", params)
		if err != nil {
			c.close()
			return nil, nil, "", fmt.Errorf("tools/list: %w", err)
		}
		var page struct {
			Tools []struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				InputSchema json.RawMessage `json:"inputSchema"`
			} `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			c.close()
			return nil, nil, "", fmt.Errorf("tools/list: %w", err)
		}
		for _, t := range page.Tools {
			name := unsafeName.ReplaceAllString(s.name+"_"+t.Name, "_")
			tools = append(tools, ToolInfo{Name: name[:min(len(name), 64)], Server: s.name, Tool: t.Name,
				Description: t.Description, Schema: t.InputSchema, Expose: s.cfg.Expose})
		}
		if cursor = page.NextCursor; cursor == "" {
			return c, tools, instructions, nil
		}
	}
}

func (m *Manager) save(s *server, c cacheFile) {
	b, err := json.Marshal(c)
	if err == nil {
		err = os.MkdirAll(m.cacheDir, 0o700)
	}
	if err == nil {
		err = os.WriteFile(m.cachePath(s.name), b, 0o600)
	}
	if err != nil {
		// Without the cache the next shell starts the server again to
		// learn its tools.
		m.note("%s: tool list not cached: %v", s.name, err)
	}
}

func (m *Manager) note(format string, args ...any) {
	m.mu.Lock()
	m.notes[fmt.Sprintf(format, args...)] = true
	m.mu.Unlock()
}

// Close stops the servers and ends the HTTP sessions, all at once: each may
// take a couple of seconds.
func (m *Manager) Close() {
	var wg sync.WaitGroup
	for _, s := range m.servers {
		s.mu.Lock()
		if s.conn != nil {
			wg.Go(s.conn.close)
		}
		s.mu.Unlock()
	}
	wg.Wait()
}

// Status is how one server is doing, for `aish mcp`.
type Status struct {
	Name      string    `json:"name"`
	Transport string    `json:"transport"` // stdio or http
	Target    string    `json:"target"`    // the command or the URL, secrets masked
	Expose    string    `json:"expose"`
	State     string    `json:"state"` // running, starting, failed, exited, idle, new
	Tools     []string  `json:"tools,omitempty"`
	Error     string    `json:"error,omitempty"`
	Failed    time.Time `json:"failed,omitzero"`
}

// StatusResult also has the notes about caches that were not written.
type StatusResult struct {
	Servers []Status `json:"servers"`
	Notes   []string `json:"notes,omitempty"`
}

func (m *Manager) Status() StatusResult {
	var res StatusResult
	for _, s := range m.servers {
		st := Status{Name: s.name, Transport: "stdio", Expose: s.cfg.Expose}
		if s.cfg.URL != "" {
			st.Transport, st.Target = "http", maskURL(s.cfg.URL)
		} else {
			st.Target = strings.Join(maskArgs(append([]string{s.cfg.Command}, s.cfg.Args...)), " ")
		}
		s.mu.Lock()
		switch {
		case s.conn != nil && s.conn.alive():
			st.State = "running"
		case s.starting != nil:
			st.State = "starting"
		case s.err != nil:
			st.State, st.Error, st.Failed = "failed", maskError(s.err.Error(), s.cfg, s.secrets), s.failed
		case s.conn != nil:
			st.State = "exited"
		case s.known:
			st.State = "idle" // tools from the cache; started on the first call
		default:
			st.State = "new"
		}
		for _, t := range s.tools {
			st.Tools = append(st.Tools, t.Name)
		}
		s.mu.Unlock()
		res.Servers = append(res.Servers, st)
	}
	m.mu.Lock()
	for n := range m.notes {
		res.Notes = append(res.Notes, n)
	}
	m.mu.Unlock()
	sort.Strings(res.Notes)
	return res
}

var secretWord = regexp.MustCompile(`(?i)token|key|secret|pass|auth|credential`)

// maskArgs hides the values of arguments that look like secrets:
// `--token X`, `--api-key=X`, `KEY=X`.
func maskArgs(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = a
		if k, _, ok := strings.Cut(a, "="); ok && secretWord.MatchString(k) {
			out[i] = k + "=***"
		} else if i > 0 && strings.HasPrefix(args[i-1], "-") && !strings.Contains(args[i-1], "=") && secretWord.MatchString(args[i-1]) {
			out[i] = "***"
		}
	}
	return out
}

// maskError hides the config's secrets in an error: a transport error may
// quote the URL with its query, a server may print its own arguments.
// extra are secrets not to be found in cfg: what its commands printed.
func maskError(msg string, cfg Server, extra []string) string {
	secrets := slices.Clone(extra)
	for i, a := range cfg.Args {
		if k, v, ok := strings.Cut(a, "="); ok && secretWord.MatchString(k) {
			secrets = append(secrets, v)
		} else if i > 0 && strings.HasPrefix(cfg.Args[i-1], "-") && secretWord.MatchString(cfg.Args[i-1]) {
			secrets = append(secrets, a)
		}
	}
	for k, v := range cfg.Env {
		if secretWord.MatchString(k) {
			secrets = append(secrets, v)
		}
	}
	for _, v := range cfg.Headers {
		secrets = append(secrets, v)
	}
	if u, err := url.Parse(cfg.URL); err == nil && cfg.URL != "" {
		if pw, ok := u.User.Password(); ok {
			secrets = append(secrets, pw)
		}
		for k, vs := range u.Query() {
			if secretWord.MatchString(k) {
				secrets = append(secrets, vs...)
			}
		}
	}
	// The longest first: a secret inside another would leave the rest of
	// that one in the open.
	slices.SortFunc(secrets, func(a, b string) int { return len(b) - len(a) })
	for _, v := range secrets {
		// Short values would mask unrelated text and are no real secrets.
		if len(v) >= 3 {
			msg = strings.ReplaceAll(msg, v, "***")
		}
	}
	return msg
}

func maskURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "***"
	}
	if u.User != nil {
		u.User = url.User(u.User.Username())
	}
	if u.RawQuery != "" {
		q := u.Query()
		for k := range q {
			if secretWord.MatchString(k) {
				q.Set(k, "***")
			}
		}
		u.RawQuery = strings.ReplaceAll(q.Encode(), "%2A%2A%2A", "***")
	}
	return u.String()
}
