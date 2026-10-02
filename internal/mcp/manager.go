package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
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
	Name        string          `json:"name"` // <server>_<tool>, also the command
	Server      string          `json:"server"`
	Tool        string          `json:"tool"` // the server's own name
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema,omitempty"`
	Expose      string          `json:"expose,omitempty"`
}

type ListParams struct {
	// Wait starts the servers whose tools are not known yet.
	Wait bool `json:"wait"`
}

type ListResult struct {
	Tools  []ToolInfo `json:"tools"`
	Errors []string   `json:"errors,omitempty"`
}

type CallParams struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

// Manager owns the MCP servers of one shell. A server is started on its
// first call; its tool list is cached on disk, so wrappers exist from the
// start of the next shell without starting anything.
type Manager struct {
	servers  []*server
	cacheDir string

	// Bin is where command wrappers go; each runs `Self tool NAME`. Taken
	// tells which names other tools already use.
	Bin, Self string
	Taken     func(name string) bool

	mu    sync.Mutex
	notes map[string]bool // wrappers not written, and why
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
	starting chan struct{}
}

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
			cfg.Expose = "commands"
		}
		b, _ := json.Marshal(cfg)
		sum := sha256.Sum256(b)
		s := &server{name: n, cfg: cfg, key: hex.EncodeToString(sum[:8])}
		var c struct {
			Key   string     `json:"key"`
			Tools []ToolInfo `json:"tools"`
		}
		if b, err := os.ReadFile(m.cachePath(n)); err == nil && json.Unmarshal(b, &c) == nil && c.Key == s.key {
			s.tools, s.known = c.Tools, true
		}
		m.servers = append(m.servers, s)
	}
	return m
}

func (m *Manager) cachePath(server string) string {
	return filepath.Join(m.cacheDir, server+".json")
}

// Warm writes the wrappers known from the cache and, in the background,
// starts the servers never seen before to learn their tools.
func (m *Manager) Warm() {
	m.wrap()
	for _, s := range m.servers {
		if !s.known {
			go m.ensure(context.Background(), s, false)
		}
	}
}

func (m *Manager) List(ctx context.Context, wait bool) ListResult {
	var res ListResult
	for _, s := range m.servers {
		s.mu.Lock()
		known := s.known
		s.mu.Unlock()
		if !known && wait {
			if _, err := m.ensure(ctx, s, false); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", s.name, err))
			}
		}
		s.mu.Lock()
		res.Tools = append(res.Tools, s.tools...)
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
	timeout := callTimeout
	if s.cfg.Timeout > 0 {
		timeout = time.Duration(s.cfg.Timeout) * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if args == nil {
		args = map[string]any{}
	}
	res, err := c.call(ctx, "tools/call", map[string]any{"name": t.Tool, "arguments": args})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", s.name, err)
	}
	return res, nil
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
		if ch := s.starting; ch != nil {
			s.mu.Unlock()
			select {
			case <-ch:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if s.err != nil && !force && time.Since(s.failed) < retryAfter {
			defer s.mu.Unlock()
			return nil, s.err
		}
		ch := make(chan struct{})
		s.starting = ch
		s.mu.Unlock()

		c, tools, err := s.connect()
		s.mu.Lock()
		s.starting = nil
		close(ch)
		if err != nil {
			s.err, s.failed = err, time.Now()
		} else {
			s.conn, s.tools, s.known, s.err = c, tools, true, nil
		}
		s.mu.Unlock()
		if err != nil {
			return nil, err
		}
		m.save(s, tools)
		m.wrap()
		return c, nil
	}
}

var unsafeName = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

func (s *server) connect() (conn, []ToolInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
	defer cancel()
	c, err := dial(s.cfg)
	if err != nil {
		return nil, nil, err
	}
	if err := initialize(ctx, c); err != nil {
		c.close()
		return nil, nil, err
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
			return nil, nil, fmt.Errorf("tools/list: %w", err)
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
			return nil, nil, fmt.Errorf("tools/list: %w", err)
		}
		for _, t := range page.Tools {
			name := unsafeName.ReplaceAllString(s.name+"_"+t.Name, "_")
			tools = append(tools, ToolInfo{Name: name[:min(len(name), 64)], Server: s.name, Tool: t.Name,
				Description: t.Description, Schema: t.InputSchema, Expose: s.cfg.Expose})
		}
		if cursor = page.NextCursor; cursor == "" {
			return c, tools, nil
		}
	}
}

func (m *Manager) save(s *server, tools []ToolInfo) {
	b, err := json.Marshal(map[string]any{"key": s.key, "tools": tools})
	if err == nil && os.MkdirAll(m.cacheDir, 0o700) == nil {
		os.WriteFile(m.cachePath(s.name), b, 0o600)
	}
}

// wrap writes a command wrapper for every known tool, except those whose
// name is a command already: the wrappers come first in PATH and would
// silently replace it for the whole shell.
func (m *Manager) wrap() {
	if m.Bin == "" {
		return
	}
	for _, s := range m.servers {
		s.mu.Lock()
		tools := s.tools
		s.mu.Unlock()
		for _, t := range tools {
			path := filepath.Join(m.Bin, t.Name)
			if _, err := os.Stat(path); err == nil {
				continue
			}
			note := ""
			if p, err := exec.LookPath(t.Name); err == nil {
				note = fmt.Sprintf("%s: not a command, it would shadow %s", t.Name, p)
			} else if m.Taken != nil && m.Taken(t.Name) {
				note = fmt.Sprintf("%s: skipped, another tool has this name", t.Name)
			}
			if note != "" {
				m.mu.Lock()
				m.notes[note] = true
				m.mu.Unlock()
				continue
			}
			script := fmt.Sprintf("#!/bin/sh\nexec %q tool %s \"$@\"\n", m.Self, t.Name)
			os.WriteFile(path, []byte(script), 0o755)
		}
	}
}

// Close stops the servers.
func (m *Manager) Close() {
	for _, s := range m.servers {
		s.mu.Lock()
		if s.conn != nil {
			s.conn.close()
		}
		s.mu.Unlock()
	}
}
