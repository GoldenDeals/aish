package proxy

import (
	"slices"

	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/policy"
)

// tellChanged says that the config files on disk differ from those in
// force: an edit waits for `aish apply-config`, and nothing else would tell
// why it changes nothing. Once for an edit of a file, not with every
// request until it is applied. Called under reqMu.
func (p *Proxy) tellChanged(conf *config.Snapshot, pols *policy.Cache, cwd string) {
	_, keys := pols.Changed()
	keys = append(keys, conf.Changed(cwd)...)
	p.mu.Lock()
	defer p.mu.Unlock()
	if !slices.ContainsFunc(keys, func(k string) bool { return !p.confSaid[k] }) {
		return
	}
	if p.confSaid == nil {
		p.confSaid = map[string]bool{}
	}
	for _, k := range keys {
		p.confSaid[k] = true
	}
	p.emit([]byte("\x1b[2m[aish: config changed on disk: aish apply-config to apply it]\x1b[0m\r\n"))
}

// unapplied names the config files on disk that differ from those in
// force, of conf and of the policies: what `aish apply-config` would apply.
func (p *Proxy) unapplied(conf *config.Snapshot) []string {
	names := conf.Stale()
	dirs, _ := p.policies.Changed()
	names = append(names, dirs...)
	return names
}
