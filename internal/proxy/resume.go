package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/inebotov/aish/internal/bashstate"
	"github.com/inebotov/aish/internal/config"
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

// restoreModel brings back the profile a session used, its model and their
// effort. A state without a model was saved before aish kept them: the
// config's stay. So do they when config.toml has the profile no more: the
// model would go to the endpoint of another. A state saved before there
// were profiles (Profile "" without TopLevel) keeps the shell's profile
// and brings back the model and the effort over it: they were of the one
// endpoint aish had then, which is now most likely the profile chosen.
// Called under p.mu.
func (p *Proxy) restoreModel(st session.Saved) {
	if st.Model == "" {
		return
	}
	if st.Profile != p.profile && (st.Profile != "" || st.TopLevel) {
		cfg, prov, err := p.toProfile(st.Profile)
		if err != nil {
			return
		}
		p.setProfile(cfg, prov)
		p.setModel(st.Model, 0)
	} else if st.Model != p.model {
		p.setModel(st.Model, 0)
	}
	// An effort of another provider would fail every request.
	if llm.CheckEffort(p.prov, st.Effort) == nil {
		p.effort = st.Effort
	}
}

// switchModel is `aish model`: the profile, the model and the effort for
// this shell at once. Called under p.mu.
func (p *Proxy) switchModel(mp rpc.ModelParams) (rpc.Info, error) {
	prov, other := p.prov, mp.Profile != p.profile
	var cfg config.Config
	if other {
		if _, err := p.snapshot().LoadProfile(mp.Profile); err != nil {
			return rpc.Info{}, notApplied(err) // one added to the file since, say
		}
		var err error
		if cfg, prov, err = p.toProfile(mp.Profile); err != nil {
			return rpc.Info{}, err
		}
	}
	if err := llm.CheckEffort(prov, mp.Effort); err != nil {
		return rpc.Info{}, err
	}
	switch {
	case other:
		p.setProfile(cfg, prov)
		p.setModel(mp.Model, mp.Window)
	case mp.Model != p.model || mp.Window > 0:
		p.setModel(mp.Model, mp.Window)
	}
	p.effort = mp.Effort
	return p.info(), nil
}

// setProfile switches to the profile of cfg, as config.LoadProfile read it,
// with prov its provider: another endpoint, other models, its own levels of
// effort and context_window. setModel goes next: the window known is of
// the old model. Called under p.mu.
func (p *Proxy) setProfile(cfg config.Config, prov llm.Provider) {
	p.profile, p.prov = cfg.Profile, prov
	p.fixedWindow, p.window = cfg.ContextWindow > 0, cfg.ContextWindow
	p.effort = ""
	if llm.CheckEffort(prov, cfg.Effort) == nil {
		p.effort = cfg.Effort
	}
}

// listProvider is the provider of cfg the shell asks for the models list
// and the levels of effort; made without the effort, which is what may
// need fixing.
func (p *Proxy) listProvider(cfg config.Config) (llm.Provider, error) {
	cfg.Effort = ""
	return p.makeProvider(cfg)
}

// makeProvider is p.newProvider, which tests set, or else llm.New.
func (p *Proxy) makeProvider(cfg config.Config) (llm.Provider, error) {
	if p.newProvider != nil {
		return p.newProvider(cfg)
	}
	return llm.New(cfg)
}

// toProfile reads profile name of config.toml, as the snapshot of it has
// it, and makes its provider for the models list and the levels of effort.
// Called under p.mu.
func (p *Proxy) toProfile(name string) (config.Config, llm.Provider, error) {
	cfg, err := p.snapshot().LoadProfile(name)
	if err != nil {
		return config.Config{}, nil, err
	}
	prov, err := p.listProvider(cfg)
	if err != nil {
		return config.Config{}, nil, err
	}
	return cfg, prov, nil
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
		go p.lookupWindow(p.prov, p.profile, model)
	}
}

// info is what `aish` commands ask the proxy about the shell. Called under
// p.mu.
func (p *Proxy) info() rpc.Info {
	return rpc.Info{SessionID: p.sess.ID, Dir: p.sess.Dir(), Saved: p.sess.Saved(),
		Profile: p.profile, Model: p.model, Effort: p.effort, Window: p.window, Asking: p.asking}
}

// modelState is the profile, the model and the effort of the shell, as
// a session keeps them. Called under p.mu.
func (p *Proxy) modelState() session.Saved {
	return session.Saved{Profile: p.profile, TopLevel: p.profile == "", Model: p.model, Effort: p.effort}
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

// resume switches the shell to session id: the journal at once, the shell
// at its next prompt, when __aish_precmd sources the script written here.
// The script is the other session's functions, aliases and variables, so
// only the user switches, as clear says.
func (p *Proxy) resume(ctx context.Context, id string) (_ rpc.Info, err error) {
	if err := session.CheckID(id); err != nil {
		return rpc.Info{}, err
	}
	defer func() {
		if err == nil {
			p.stopBackground() // with the session they belong to
		}
	}()
	fg := p.fromShell(ctx)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.asking {
		return rpc.Info{}, errors.New("sessions are switched by the user, not by the assistant")
	}
	if fg != nil {
		return rpc.Info{}, fg
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
	// CheckID already keeps quotes out; the quoting is a second line.
	script := bashstate.Script(change) + "export AISH_SESSION='" + next.ID + "'\n"
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
