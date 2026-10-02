package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/inebotov/aish/internal/bashstate"
	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/rpc"
	"github.com/inebotov/aish/internal/session"
)

// Resume makes the shell about to start come back as the session left it:
// set before Run.
func (p *Proxy) Resume(st session.Saved) {
	if st.Shell.Cwd == "" {
		st.Shell.Cwd = p.sess.LastCwd()
	}
	p.restore = bashstate.Script(st.Shell)
	p.resumed = &st
}

// restoreModel brings back the model a session used and its effort. A state
// without a model was saved before aish kept them: the config's stay. Called
// under p.mu.
func (p *Proxy) restoreModel(st session.Saved) {
	if st.Model == "" {
		return
	}
	if st.Model != p.model {
		p.setModel(st.Model, 0)
	}
	// An effort of another provider would fail every request.
	if llm.CheckEffort(p.provName, st.Effort) == nil {
		p.effort = st.Effort
	}
}

// setModel switches the model; window 0 has its size looked up. Called under
// p.mu.
func (p *Proxy) setModel(model string, window int) {
	p.model = model
	if p.fixedWindow {
		return
	}
	p.window = window
	if window == 0 {
		go p.lookupWindow(model)
	}
}

// info is what `aish` commands ask the proxy about the shell. Called under
// p.mu.
func (p *Proxy) info() rpc.Info {
	return rpc.Info{SessionID: p.sess.ID, Model: p.model, Effort: p.effort, Window: p.window}
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
		base, err := bashstate.Parse(b, "")
		if err != nil {
			return
		}
		p.base = &base
	}
	b, err := os.ReadFile(filepath.Join(p.run, "state"))
	if err != nil {
		return
	}
	cur, err := bashstate.Parse(b, cwd)
	if err != nil {
		return
	}
	p.cur = &cur
	if p.sess.Len() == 0 {
		return // a session is its journal: no state without one
	}
	saved := session.Saved{Shell: bashstate.Diff(*p.base, cur), Model: p.model, Effort: p.effort}
	data, _ := json.Marshal(saved)
	key := append([]byte(p.sess.ID), data...)
	if bytes.Equal(key, p.lastSaved) {
		return
	}
	if err := session.SaveState(p.sess.Dir(), p.sess.ID, saved); err == nil {
		p.lastSaved = key
	}
}

// resume switches the shell to session id: the journal at once, the shell
// at its next prompt, when __aish_precmd sources the script written here.
func (p *Proxy) resume(id string) (rpc.Info, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.asking {
		return rpc.Info{}, errors.New("sessions are switched by the user, not by the assistant")
	}
	if id == p.sess.ID {
		return rpc.Info{}, fmt.Errorf("already in session %s", id)
	}
	dir := p.sess.Dir()
	next, err := session.Load(dir, id)
	if err != nil {
		return rpc.Info{}, err
	}
	saved, err := session.LoadState(dir, id)
	if err != nil {
		return rpc.Info{}, err
	}
	if err := next.Lock(); err != nil {
		return rpc.Info{}, err
	}
	if saved.Shell.Cwd == "" {
		saved.Shell.Cwd = next.LastCwd()
	}
	// The change from this shell as it is now to how it started plus what
	// the other session changed: this session's own changes are undone.
	change := saved.Shell
	if p.base != nil && p.cur != nil {
		change = bashstate.Diff(*p.cur, bashstate.Apply(*p.base, saved.Shell))
	}
	script := bashstate.Script(change) + "export AISH_SESSION=" + id + "\n"
	if err := os.WriteFile(filepath.Join(p.run, "restore.bash"), []byte(script), 0o600); err != nil {
		next.Unlock()
		return rpc.Info{}, err
	}
	p.sess.Unlock()
	p.sess = next
	p.folds = nil
	p.switched = true
	p.restoreModel(saved)
	return p.info(), nil
}

// session is the current session for code that does not hold p.mu.
func (p *Proxy) session() *session.Session {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sess
}
