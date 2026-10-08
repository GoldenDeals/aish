package proxy

import (
	"context"
	"fmt"
	"time"

	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/policy"
	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/tools"
)

// The commands in the shell — `aish status`, `aish model`, `aish policy`,
// `aish hooks`, `aish tool` — show and go by the config in force, the one
// requests go by, rather than by the files on disk, which may hold an edit
// not applied yet: rpc config, models and policy answer them from the
// snapshot and the policies the proxy keeps (see applyconfig.go). The API
// keys stay here: the models are listed by the proxy, and the config a
// command gets has none.

// modelsTimeout bounds the listing of the models, as `aish model` bounded
// it when it asked the API itself.
const modelsTimeout = 15 * time.Second

// configFor is rpc config: the config a request of the shell from cp.Cwd
// would go by, as prepare makes it, for the shell's profile or the one
// asked for, which is as LoadProfile has it. The shell's, when it is the
// one config.toml selects for its environment, is as LoadEnv has it,
// $AISH_MODEL and $AISH_EFFORT laid over: what the shell started with is
// not a switch.
func (p *Proxy) configFor(ctx context.Context, cp rpc.ConfigParams) (rpc.Config, error) {
	getenv := tools.Exec{Env: cp.Env}.Getenv
	p.mu.Lock()
	conf := p.snapshot()
	profile := p.profile
	p.mu.Unlock()
	if cp.Profile != nil {
		profile = *cp.Profile
	}
	var res rpc.Config
	def, defErr := conf.LoadEnv(getenv)
	if defErr != nil {
		res.DefaultErr = defErr.Error()
	} else {
		res.Default = def.Profile
	}
	cfg := def
	if defErr != nil || cp.Profile != nil || def.Profile != profile {
		var err error
		if cfg, err = conf.LoadProfile(profile); err != nil {
			return rpc.Config{}, notApplied(err)
		}
	}
	res.Global = rulesOf(cfg).Len()
	cfg, project, err := conf.Project(cfg, cp.Cwd)
	if err != nil {
		return rpc.Config{}, err
	}
	res.Project = project
	if cp.Policies {
		if eng, err := p.policies.Engine(ctx, cfg.PolicyDir, rulesOf(cfg)); err != nil {
			res.PolicyErr = err.Error()
		} else {
			res.Policies = eng.Summary()
		}
	}
	res.Changed = p.unapplied(conf)
	res.Config = withoutSecrets(cfg)
	return res, nil
}

// withoutSecrets is cfg for a command in the shell: without the API keys
// and the proxies of the requests, which may carry a password, and with
// the profiles by name only. They are of no use there: the models are
// listed here.
func withoutSecrets(cfg config.Config) config.Config {
	cfg.APIKey = ""
	cfg.HTTPProxy, cfg.HTTPSProxy, cfg.AllProxy, cfg.NoProxy = "", "", "", ""
	if cfg.Profiles != nil {
		names := make(map[string]config.Profile, len(cfg.Profiles))
		for n := range cfg.Profiles {
			names[n] = config.Profile{}
		}
		cfg.Profiles = names
	}
	return cfg
}

// notApplied is err of a profile the config in force has not, which the
// file on disk may have: `aish model` was given a profile added since.
func notApplied(err error) error {
	return fmt.Errorf("%w, as aish applied it; aish apply-config applies config.toml as it is now", err)
}

// models is rpc models: the models of a profile of the config in force,
// asked with its key, or the one of the shell's environment, as a request
// would be.
func (p *Proxy) models(ctx context.Context, mp rpc.ModelsParams) ([]llm.ModelInfo, error) {
	p.mu.Lock()
	conf := p.snapshot()
	p.mu.Unlock()
	cfg, err := conf.LoadProfile(mp.Profile)
	if err != nil {
		return nil, notApplied(err)
	}
	cfg.APIKey = llm.Key(cfg, tools.Exec{Env: mp.Env}.Getenv)
	prov, err := p.listProvider(cfg)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, modelsTimeout)
	defer cancel()
	return prov.Models(ctx)
}

// checkPolicy is rpc policy, `aish policy TOOL ARGS`: what the policies a
// request of the shell from pp.Cwd would go by say of the call, made by
// the shell's model, read as the shell reads it in the modes of its
// options (see shellExec).
func (p *Proxy) checkPolicy(ctx context.Context, pp rpc.PolicyParams) (policy.Decision, error) {
	p.mu.Lock()
	conf := p.snapshot()
	profile, model, opts := p.profile, p.model, p.shellOpts()
	p.mu.Unlock()
	cfg, err := conf.LoadProfile(profile)
	if err != nil {
		return policy.Decision{}, err
	}
	if cfg, _, err = conf.Project(cfg, pp.Cwd); err != nil {
		return policy.Decision{}, err
	}
	eng, err := p.policies.Engine(ctx, cfg.PolicyDir, rulesOf(cfg))
	if err != nil {
		return policy.Decision{}, err
	}
	in := policy.NewInputIn(p.shell.Name(), pp.Tool, pp.Args, pp.Cwd, pp.Env, opts...)
	if pp.HandOff {
		in.HandOff(pp.Line)
	}
	in.Server, in.Model, in.Agent = pp.Server, model, pp.Agent
	return eng.Check(ctx, in)
}
