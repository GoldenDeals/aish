package proxy

import (
	"context"
	"errors"

	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/policy"
	"github.com/GoldenDeals/aish/internal/rpc"
)

// errTrustAsks refuses the assistant `aish trust`, as the guard does by
// the text of its lines.
var errTrustAsks = errors.New(policy.TrustReason)

// trust is rpc trust, `aish trust` inside aish: the project file of the
// client's directory, as it is on disk now, trusted once the user said Yes
// to the question that names it and its keys that run code. The file, the
// keys and the sum trusted are read before the question: an edit while it
// is open is not trusted (config.TrustAs). Only the user, at the shell's
// foreground, trusts, as with aish apply-config (userconfirm.go).
func (p *Proxy) trust(ctx context.Context, tp rpc.TrustParams) (rpc.Trusted, error) {
	fg := p.fromShell(ctx)
	p.mu.Lock()
	switch {
	case p.asking:
		p.mu.Unlock()
		return rpc.Trusted{}, errTrustAsks
	case fg != nil:
		p.mu.Unlock()
		return rpc.Trusted{}, fg
	}
	conf, profile := p.snapshot(), p.profile
	p.mu.Unlock()

	// The file is checked over the config requests go by: one aish
	// refuses is no use trusted.
	cfg, err := conf.LoadProfile(profile)
	if err != nil {
		if cfg, err = conf.LoadProfile(""); err != nil {
			return rpc.Trusted{}, err
		}
	}
	_, path, err := config.Project(cfg, tp.Cwd)
	switch {
	case path == "" && err == nil:
		return rpc.Trusted{}, errors.New("no " + config.ProjectFile + " here")
	case err != nil:
		return rpc.Trusted{}, err
	}
	sum := config.Sum(path) // before the keys: an edit between them is refused
	keys, err := config.CodeKeys(path)
	if err != nil {
		return rpc.Trusted{}, err
	}
	if err := p.userConfirms(ctx, trustQuestion(path, keys), shortPath(path)+" not trusted"); err != nil {
		return rpc.Trusted{}, err
	}
	p.mu.Lock()
	asking := p.asking
	p.mu.Unlock()
	if asking {
		return rpc.Trusted{}, errTrustAsks // a request began meanwhile: not the user's to answer for
	}
	if err := config.TrustAs(path, sum); err != nil {
		return rpc.Trusted{}, err
	}
	return rpc.Trusted{Path: path, Keys: keys}, nil
}
