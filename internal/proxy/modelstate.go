package proxy

import (
	"context"
	"time"

	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/session"
)

// modelState is what this shell's requests go to: the profile of
// config.toml, the model and the effort, which `aish model` switches for
// the shell and a session keeps, and the size of the model's context.
// Under p.mu, but for newProvider, which tests set before anything runs.
type modelState struct {
	profile     string       // "" for the top level of config.toml
	model       string       // `aish model` switches it for this shell
	effort      string       // "" being the model's default
	window      int          // the model's context size, 0 if unknown
	fixedWindow bool         // context_window is set in the config
	prov        llm.Provider // the profile's, for the models list and the levels of effort; nil if unknown

	// defProfile is the one config.toml selects, as the last request (or
	// the start) read it, which the status does not name.
	defProfile  string
	defErr      string // why config.toml selects none, as tellDefErr told it last
	windowAsked string // what lookupOnce last asked about, the key included

	newProvider func(config.Config) (llm.Provider, error) // nil: llm.New; tests set it
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
func (m *modelState) setProfile(cfg config.Config, prov llm.Provider) {
	m.profile, m.prov = cfg.Profile, prov
	m.fixedWindow, m.window = cfg.ContextWindow > 0, cfg.ContextWindow
	m.effort = ""
	if llm.CheckEffort(prov, cfg.Effort) == nil {
		m.effort = cfg.Effort
	}
}

// listProvider is the provider of cfg the shell asks for the models list
// and the levels of effort; made without the effort, which is what may
// need fixing.
func (m *modelState) listProvider(cfg config.Config) (llm.Provider, error) {
	cfg.Effort = ""
	return m.makeProvider(cfg)
}

// makeProvider is p.newProvider, which tests set, or else llm.New.
func (m *modelState) makeProvider(cfg config.Config) (llm.Provider, error) {
	if m.newProvider != nil {
		return m.newProvider(cfg)
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

// savedModel is the profile, the model and the effort of the shell, as
// a session keeps them. Called under p.mu.
func (m *modelState) savedModel() session.Saved {
	return session.Saved{Profile: m.profile, TopLevel: m.profile == "", Model: m.model, Effort: m.effort}
}

// lookupWindow asks prov, the provider of profile, for the context size of
// model, which the models list reports for Anthropic. The shell may have
// switched to another profile or model by the time it answers.
func (p *Proxy) lookupWindow(prov llm.Provider, profile, model string) {
	if prov == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ms, err := prov.Models(ctx)
	if err != nil {
		return
	}
	for _, m := range ms {
		if m.ID == model && m.Window > 0 {
			p.mu.Lock()
			if p.profile == profile && p.model == model && p.window == 0 {
				p.window = m.Window
			}
			p.mu.Unlock()
		}
	}
}

// lookupOnce has the window looked up with prov, the agent's provider,
// whose key is the shell's: the proxy's own provider reads the proxy's
// environment, where the key may not be. Once for a profile, a model and
// a key, so that an API which reports no window is not asked each turn;
// prepare forgets the last one asked about once a window is known, so
// that a shell back on that model with no window, after `aish resume`,
// asks anew. Called under p.mu.
func (p *Proxy) lookupOnce(prov llm.Provider, profile, model, key string) {
	asked := profile + "\x00" + model + "\x00" + key
	if asked == p.windowAsked {
		return
	}
	p.windowAsked = asked
	go p.lookupWindow(prov, profile, model)
}
