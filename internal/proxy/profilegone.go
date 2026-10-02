package proxy

import (
	"fmt"

	"github.com/inebotov/aish/internal/config"
	"github.com/inebotov/aish/internal/llm"
)

// leaveGone moves the shell off a profile config.toml has no more, to the
// one def, config.toml as Load read it, selects, with that profile's own
// model and effort, and tells the user: the profile renamed or removed
// would fail every request, with no way back but a restart once
// config.toml has no profiles left. Called under p.mu.
func (p *Proxy) leaveGone(def config.Config) error {
	cfg, prov, err := p.toProfile(def.Profile)
	if err != nil {
		return err
	}
	gone := p.profile
	p.setProfile(cfg, prov)
	p.setModel(cfg.Model, 0)
	now := p.profile
	if now == "" {
		now = config.Root
	}
	p.emit(fmt.Appendf(nil, "\x1b[2m[aish: profile %s is not in config.toml: on %s]\x1b[0m\r\n", gone, now))
	return nil
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
