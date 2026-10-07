package proxy

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/GoldenDeals/aish/internal/session"
	"github.com/GoldenDeals/aish/internal/shells"
	"github.com/GoldenDeals/aish/internal/shellstate"
)

// shellState is the shell's side: which shell it is, the directory aish
// runs it with, its process, and its state — variables, functions,
// aliases, options and cwd — as __aish_precmd dumps it at every prompt,
// before cmd-end. Run sets shell and run before the shell starts; the rest
// is under p.mu.
type shellState struct {
	shell shells.Shell        // New sets bash, Run the configured one
	run   string              // $AISH_RUN
	fg    func() (int, error) // the shell's foreground process group (peer.go); nil before Run, tests set it

	// How the shell started, how it was at the last prompt, and what of it
	// was saved last (session id and all).
	base, cur *shellstate.State
	lastSaved []byte
	switched  bool // `aish resume` switched the session during this command

	resumed *session.Saved // the session the shell comes back as, set before Run
}

// shellOpts are the names of the options the shell had on at its last
// prompt; nil before the first. Called under p.mu.
func (s *shellState) shellOpts() []string {
	if s.cur == nil {
		return nil
	}
	return s.cur.On()
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
		base, err := p.shell.ParseState(b, "", p.stateIgnore)
		if err != nil {
			return
		}
		p.base = &base
	}
	b, err := os.ReadFile(filepath.Join(p.run, "state"))
	if err != nil {
		return
	}
	cur, err := p.shell.ParseState(b, cwd, p.stateIgnore)
	if err != nil {
		return
	}
	p.cur = &cur
	if !p.sess.Saved() {
		return // an unsaved session leaves nothing on disk, its state neither
	}
	saved := p.savedModel()
	saved.Shell = shellstate.Diff(*p.base, cur)
	data, _ := json.Marshal(saved)
	key := append([]byte(p.sess.ID), data...)
	if bytes.Equal(key, p.lastSaved) {
		return
	}
	if err := session.SaveState(p.sess.Dir(), p.sess.ID, saved); err == nil {
		p.lastSaved = key
	}
}
