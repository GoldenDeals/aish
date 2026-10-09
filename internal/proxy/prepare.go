package proxy

import (
	"context"
	"fmt"

	"github.com/GoldenDeals/aish/internal/agent"
	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/mcp"
	"github.com/GoldenDeals/aish/internal/skills"
	"github.com/GoldenDeals/aish/internal/subagent"
	"github.com/GoldenDeals/aish/internal/tools"
)

// prepare gives the agent what this request needs: the config as the
// snapshot of the config files has it (see applyconfig.go), with this
// shell's profile, model and effort and the project file of its directory;
// the provider, kept while they stay the same; the policies, compiled the
// first time a request needs them; and, for a fresh request, the tools of
// the shell's directory; for a later step of one, its tools and hooks anew
// once the project is no longer trusted.
func (p *Proxy) prepare(ctx context.Context, ex tools.Exec, fresh bool) (*agent.Agent, error) {
	p.mu.Lock()
	conf := p.snapshot()
	// What config.toml selects, by $AISH_PROFILE as the shell has it now:
	// what the status compares the shell's profile with, and where a shell
	// goes whose profile is gone. It fails where the shell's profile may
	// not, on a profile $AISH_PROFILE or the profile key names and
	// config.toml has not: tellDefErr tells of it, and a shell whose
	// profile is gone too goes to the top level.
	def, defErr := conf.LoadEnv(ex.Getenv)
	if defErr == nil {
		p.defProfile = def.Profile
	}
	if err := p.leaveGone(def, defErr); err != nil {
		p.mu.Unlock()
		return nil, err
	}
	// One moment for all of them: `aish model` may land in between. The
	// shell's profile, not the one config.toml selects, which `aish model`
	// may have switched from.
	profile, model, effort, window, fixed := p.profile, p.model, p.effort, p.window, p.fixedWindow
	if window > 0 {
		p.windowAsked = "" // see lookupOnce
	}
	p.mu.Unlock()
	cfg, err := conf.LoadProfile(profile)
	if err != nil {
		return nil, err
	}
	if fresh {
		p.tellDefErr(defErr)
		p.tellConfigPath(ex.Getenv)
		p.tellChanged(conf, &p.policies, ex.Dir)
	}
	cfg, project, err := conf.Project(cfg, ex.Dir)
	if err != nil {
		return nil, err
	}
	if fresh {
		p.tellUntrusted(project, cfg.Untrusted)
	}
	p.mu.Lock()
	p.project = project
	// The agent opens the line of a call by it, and the output under the
	// call is folded by the same: the project's value, not config.toml's.
	p.foldLines = cfg.FoldLines
	cfg.Model, cfg.Effort = model, effort
	if cfg.ContextWindow == 0 {
		cfg.ContextWindow = window // the API's or `aish model`'s, for compact_at
	}
	a := p.ag
	if a == nil {
		a = &agent.Agent{Journal: journal{p}, Shell: shell{p}, UI: &ui{p: p}, Yolo: p.yoloOn}
		p.ag = a
	}
	p.mu.Unlock()
	// The key as the shell has it: the user may have exported it there.
	cfg.APIKey = llm.Key(cfg, ex.Getenv)
	prov, err := p.providerFor(cfg)
	if err != nil {
		return nil, err
	}
	// Not waited for: the first request must not stand on the models list.
	// The window reaches cfg with the next prepare.
	if window == 0 && !fixed {
		p.mu.Lock()
		p.lookupOnce(prov, profile, model, cfg.APIKey)
		p.mu.Unlock()
	}
	pol, err := p.policies.Engine(ctx, cfg.PolicyDir, rulesOf(cfg))
	if err != nil {
		return nil, err
	}
	// The project's code was on in the last step and its trust is gone
	// now: a git pull the agent ran, say. The rest of the request goes
	// without it, as the next request would: the hooks and tools found
	// when it began run the files as they are now.
	distrusted := !fresh && len(a.Cfg.Untrusted) == 0 && len(cfg.Untrusted) > 0 &&
		(a.Cfg.HooksDir != cfg.HooksDir || a.Cfg.ToolsDir != cfg.ToolsDir)
	a.Cfg, a.Provider, a.Policy = cfg, prov, pol
	if fresh || a.Tools == nil || distrusted {
		a.Tools = p.loadTools(ctx, cfg, ex.Dir)
		defs, _ := subagent.Find(ex.Dir)
		a.AddSubagents(defs)
	}
	if distrusted {
		fmt.Fprintf(a.UI, "\x1b[2m[aish: %s or its hooks/tools changed: project code is off until aish trust]\x1b[0m\n", config.ProjectFile)
		a.ResetHooks()
	}
	return a, nil
}

// providerFor is the provider for cfg, the last one while nothing it
// depends on changed, so that its connections are kept between turns.
func (p *Proxy) providerFor(cfg config.Config) (llm.Provider, error) {
	key := fmt.Sprint(cfg.Provider, "\x00", cfg.BaseURL, "\x00", cfg.APIKey, "\x00", cfg.Model, "\x00", cfg.Effort, "\x00", cfg.MaxTokens)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.agentProv != nil && p.agentProvKey == key {
		return p.agentProv, nil
	}
	prov, err := p.makeProvider(cfg)
	if err != nil {
		return nil, err
	}
	p.agentProv, p.agentProvKey = prov, key
	return prov, nil
}

// loadTools returns the built-in and external tools, the skills of cwd
// the model may invoke, and the MCP tools known so far: waiting for a
// server still warming up would stall the request with no output. Such a
// server reaches the model with the next request.
func (p *Proxy) loadTools(ctx context.Context, cfg config.Config, cwd string) *tools.Registry {
	reg := tools.Load(cfg.ToolsDir)
	found, _ := skills.Find(cwd)
	for _, s := range found {
		if !s.UserOnly {
			reg.Add(s.Tool())
		}
	}
	local, _ := mcp.Local(ctx, p.mcp, false)
	for _, t := range local {
		reg.Add(t)
	}
	return reg
}
