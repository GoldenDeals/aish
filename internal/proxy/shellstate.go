package proxy

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/GoldenDeals/aish/internal/bashstate"
	"github.com/GoldenDeals/aish/internal/session"
)

// shellOpts are the names of the options the shell had on at its last
// prompt; nil before the first. Called under p.mu.
func (p *Proxy) shellOpts() []string {
	if p.cur == nil {
		return nil
	}
	var on []string
	for name, line := range p.cur.Opts {
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
	saved := p.modelState()
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
