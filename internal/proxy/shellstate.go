package proxy

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/GoldenDeals/aish/internal/bashstate"
	"github.com/GoldenDeals/aish/internal/session"
)

// shellState is the shell's side: the directory aish runs it with, its
// process, and its state — variables, functions, aliases, options and
// cwd — as __aish_precmd dumps it at every prompt, before cmd-end. Run
// sets run before the shell starts; the rest is under p.mu.
type shellState struct {
	run string              // $AISH_RUN
	fg  func() (int, error) // the shell's foreground process group (peer.go); nil before Run, tests set it

	// How the shell started, how it was at the last prompt, and what of it
	// was saved last (session id and all).
	base, cur *bashstate.State
	lastSaved []byte
	switched  bool // `aish resume` switched the session during this command

	restore string // the script that brings back a resumed session
	resumed *session.Saved
}

// shellOpts are the names of the options the shell had on at its last
// prompt; nil before the first. Called under p.mu.
func (s *shellState) shellOpts() []string {
	if s.cur == nil {
		return nil
	}
	var on []string
	for name, line := range s.cur.Opts {
		if line == "set -o "+name || line == "shopt -s "+name {
			on = append(on, name)
		}
	}
	return on
}

// saveState records how the shell differs from the one that started, from
// what __aish_precmd dumped right before cmd-end. Called under p.mu.
func (p *Proxy) saveState(cwd string) {
	if p.run == "" {
		return
	}
	if p.base == nil {
		b, err := os.ReadFile(filepath.Join(p.run, "state.base"))
		if err != nil {
			return
		}
		base, err := bashstate.Parse(b, "", p.stateIgnore)
		if err != nil {
			return
		}
		p.base = &base
	}
	b, err := os.ReadFile(filepath.Join(p.run, "state"))
	if err != nil {
		return
	}
	cur, err := bashstate.Parse(b, cwd, p.stateIgnore)
	if err != nil {
		return
	}
	p.cur = &cur
	if !p.sess.Saved() {
		return // an unsaved session leaves nothing on disk, its state neither
	}
	saved := p.savedModel()
	saved.Shell = bashstate.Diff(*p.base, cur)
	data, _ := json.Marshal(saved)
	key := append([]byte(p.sess.ID), data...)
	if bytes.Equal(key, p.lastSaved) {
		return
	}
	if err := session.SaveState(p.sess.Dir(), p.sess.ID, saved); err == nil {
		p.lastSaved = key
	}
}
