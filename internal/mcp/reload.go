package mcp

import "errors"

// errStopped is what a call to a server Reload let go gets, and the start
// of it that was under way: the server is not the config's any more.
var errStopped = errors.New("stopped: the MCP config changed (aish apply-config)")

// Reload makes the servers of cfgs, the MCP config read anew by `aish
// apply-config`, the manager's. A server whose config is the same stays as
// it is, running or not, with the tools and instructions it knows; one
// whose config changed, or that cfgs has no more, is stopped, and a call
// to it fails. The rest are new and known from the cache as NewManager
// knows them; they start as at aish's start, by Warm or by their first
// call. The agent finds the new set with its next request: it lists the
// manager's tools for each.
func (m *Manager) Reload(cfgs map[string]Server) {
	// The keys and the caches as NewManager has them, read before the lock.
	next := NewManager(cfgs, m.cacheDir).servers
	m.mu.Lock()
	gone := map[string]*server{}
	for _, s := range m.servers {
		gone[s.name] = s
	}
	for i, s := range next {
		if old, ok := gone[s.name]; ok && old.key == s.key {
			next[i] = old
			delete(gone, s.name)
		}
	}
	m.servers = next
	m.mu.Unlock()
	for _, s := range gone {
		s.stop()
	}
}

// list is the servers as they are now; Reload replaces the slice, it does
// not change one handed out.
func (m *Manager) list() []*server {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.servers
}

// stop lets the server go: what its connection holds, a process or a
// session at the server, is closed without anyone waiting for it, and a
// start under way closes what it gets.
func (s *server) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = true
	if s.conn != nil {
		go s.conn.close()
		s.conn = nil
	}
}
