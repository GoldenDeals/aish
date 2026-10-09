package proxy

import (
	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/session"
)

// contextSize is the size of the context the next request of the shell
// would send, as the agent counts it (session.Tokens): by the config a
// request from the shell's directory would go by (sizeConfig), with the
// system prompt and the tool schemas of the last request. cfg is that
// config, with the window and compact_at the status marks by. Called
// under p.mu.
func (p *Proxy) contextSize(es []session.Entry) (est session.Estimate, cfg config.Config) {
	cfg = p.sizeConfig(p.shellDir())
	return session.Tokens(es, cfg.MaxOutputBytes, p.overhead), cfg
}

// sizeConfig is the config a request of the shell from cwd would size its
// context by, as prepare gives it to the agent: max_output_bytes and
// compact_at of config.toml (applyFields; a profile sets neither), the
// project file of cwd over them as far as the snapshot has read it
// (config.Snapshot.Known), and the window of the shell's model, which is
// the profile's context_window when it has one. Called under p.mu.
func (p *Proxy) sizeConfig(cwd string) config.Config {
	cfg := config.Default()
	cfg.MaxOutputBytes, cfg.CompactAt = p.maxOutput, p.compactAt
	if p.conf != nil {
		cfg = p.conf.Known(cfg, cwd)
	}
	cfg.ContextWindow = p.window
	return cfg
}

// shellDir is the shell's directory at its last prompt, or that of the
// last command in the journal before the first. Called under p.mu.
func (p *Proxy) shellDir() string {
	if p.cur != nil {
		return p.cur.Cwd
	}
	return p.sess.LastCwd()
}
