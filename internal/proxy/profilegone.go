package proxy

import (
	"fmt"

	"github.com/GoldenDeals/aish/internal/config"
)

// leaveGone moves the shell off a profile config.toml has no more, to the
// one def, config.toml as LoadEnv read it, selects, with that profile's
// own model and effort, and tells the user: the profile renamed or removed
// would fail every request, with no way back but a restart once
// config.toml has no profiles left. A selected profile config.toml has
// not either (defErr), or one that cannot be had, sends the shell to the
// top level, which config.toml always has; a config.toml that cannot be
// read at all is the request's error. Called under p.mu.
func (p *Proxy) leaveGone(def config.Config, defErr error) error {
	if _, ok := def.Profiles[p.profile]; p.profile == "" || ok {
		return nil
	}
	to := def.Profile
	if defErr != nil {
		to = ""
	}
	cfg, prov, err := p.toProfile(to)
	if err != nil && to != "" {
		cfg, prov, err = p.toProfile("")
	}
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
