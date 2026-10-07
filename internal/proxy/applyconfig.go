package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/inebotov/aish/internal/config"
	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/mcp"
	"github.com/inebotov/aish/internal/policy"
	"github.com/inebotov/aish/internal/rpc"
)

// The config files — config.toml with its profiles, the project files,
// the policies, the MCP config — are read as aish starts and when the
// user runs `aish apply-config`, not in between: a request goes by
// p.conf, the snapshot of them, by p.policies, compiled from them once and
// kept until then, and by the servers of p.mcp. An edit is in force all at
// once and when the user says so, rather than one key from the next
// request and another only after a restart. What the proxy takes for
// itself (applyFields) comes from the same snapshot. Trust in a project
// file is the exception: it is checked against the disk at every use (see
// config.Snapshot), so that an edit turns the project's code off at once.
//
// The config a request of the shell from a directory goes by is what
// prepare makes of p.snapshot(): LoadProfile of the shell's profile,
// Project of the directory over it, the shell's model and effort. The
// commands in the shell show that one too (inforce.go), not the files.

// snapshot is the snapshot of the config files requests go by. A proxy
// that Run has not given one, a test's, takes it at the first need.
// Called under p.mu.
func (p *Proxy) snapshot() *config.Snapshot {
	if p.conf == nil {
		p.conf = config.NewSnapshot()
	}
	return p.conf
}

// applyFields sets what the proxy takes from config.toml for itself, the
// shell's side of the requests. A new state_ignore has the base of the
// shell's state parsed again (saveState): a base and a state parsed by
// different lists would differ by the variables between the lists, and the
// session would keep them set, or unset, for nothing. Called under p.mu.
func (p *Proxy) applyFields(cfg config.Config) {
	p.foldLines = cfg.FoldLines
	p.maxOutput = cfg.MaxOutputBytes
	p.promptStatus = cfg.PromptStatus
	p.compactAt = cfg.CompactAt
	p.ignore = cfg.JournalIgnore
	if !slices.Equal(p.stateIgnore, cfg.StateIgnore) {
		p.stateIgnore = cfg.StateIgnore
		p.base = nil
	}
}

// rulesOf are the simple rules of cfg's [policy].
func rulesOf(cfg config.Config) policy.Rules {
	return policy.Rules{Deny: cfg.Policy.Deny, Ask: cfg.Policy.Ask, WriteOutsideHome: cfg.Policy.WriteOutsideHome,
		Hints: cfg.Policy.Hints, WriteOutsideHomeHint: cfg.Policy.WriteOutsideHomeHint}
}

// errApplyAsks refuses the assistant `aish apply-config`: the config is
// where the user's policies and the endpoint the requests go to are, and an
// edit the assistant made would be in force without the user knowing.
var errApplyAsks = errors.New("the config is applied by the user, not by the assistant")

// applyConfig is `aish apply-config`: the config files read anew and in
// force at once, or, if any of them is broken, not at all — config.toml,
// the profile the shell goes to, the project file of the shell's directory
// and the policies there are checked before anything changes. The shell's
// profile, model and effort follow config.toml where they are what it gave
// them (follow). Only the user, at the shell's foreground, applies: as with
// `aish model`, the assistant is refused by p.asking, a process in the
// background by fromShell.
func (p *Proxy) applyConfig(ctx context.Context, ap rpc.AgentParams) (rpc.Applied, error) {
	fg := p.fromShell(ctx)
	p.mu.Lock()
	switch {
	case p.asking:
		p.mu.Unlock()
		return rpc.Applied{}, errApplyAsks
	case fg != nil:
		p.mu.Unlock()
		return rpc.Applied{}, fg
	}
	old := p.snapshot()
	sh := shellModel{profile: p.profile, model: p.model, effort: p.effort, def: p.defProfile}
	started, mcpFile, mcpSum := p.started, p.mcpFile, p.mcpSum
	p.mu.Unlock()

	next := config.NewSnapshot()
	top, err := next.LoadProfile("")
	if err != nil {
		return rpc.Applied{}, fmt.Errorf("%w; nothing applied", err)
	}
	def, defErr := next.LoadEnv(execOf(ap).Getenv)
	to, err := p.follow(old, next, top, def, defErr, sh)
	if err != nil {
		return rpc.Applied{}, fmt.Errorf("%w; nothing applied", err)
	}
	cfg, _, err := next.Project(to.cfg, ap.Cwd)
	if err != nil {
		return rpc.Applied{}, fmt.Errorf("%w; nothing applied", err)
	}
	// Compiled into the cache put in force: what was checked is what runs.
	fresh := new(policy.Cache)
	if _, err := fresh.Engine(ctx, cfg.PolicyDir, rulesOf(cfg)); err != nil {
		return rpc.Applied{}, fmt.Errorf("%w; nothing applied", err)
	}
	// The MCP config, read anew if it is another file or was edited; one
	// that does not parse is not applied, as config.toml is not. The sum
	// is taken first: an edit while the file is read is told of
	// afterwards rather than missed.
	mcpNow := fileSum(top.MCPConfig)
	reloadMCP := top.MCPConfig != mcpFile || mcpNow != mcpSum
	var servers map[string]mcp.Server
	if reloadMCP {
		if servers, err = mcp.LoadConfig(top.MCPConfig); err != nil {
			return rpc.Applied{}, fmt.Errorf("%w; nothing applied", err)
		}
	}

	res := rpc.Applied{Keys: old.Keys(next)}
	for _, f := range old.Stale() {
		if f != old.Path() { // its keys are named
			res.Files = append(res.Files, f)
		}
	}
	dirs, _ := p.policies.Changed()
	res.Files = append(res.Files, dirs...)
	if mcpNow != mcpSum { // another file alone is named by its key
		res.Files = append(res.Files, top.MCPConfig)
	}
	was := top
	if started != nil {
		was = *started
	} else if c, err := old.LoadProfile(""); err == nil {
		was = c
	}
	res.Restart = restartKeys(was, top)

	p.mu.Lock()
	defer p.mu.Unlock()
	p.conf = next
	p.policies.Reset(fresh)
	p.applyFields(top)
	if defErr == nil {
		p.defProfile = def.Profile
	}
	before := p.info()
	if to.gone {
		// As a request would, with the line saying why; follow found it
		// a place to go.
		if err := p.leaveGone(def, defErr); err != nil {
			return rpc.Applied{}, err
		}
	} else {
		window := 0 // looked up anew, unless the model and its window stay
		if to.model == p.model && to.cfg.Profile == p.profile && !p.fixedWindow {
			window = p.window
		}
		p.setProfile(to.cfg, to.prov)
		p.setModel(to.model, window)
		p.effort = to.effort
	}
	if reloadMCP {
		// The servers that stay go on as they are; the agent lists the
		// new set with its next request.
		p.mcp.Reload(servers)
		p.mcp.Warm()
		p.mcpFile, p.mcpSum = top.MCPConfig, mcpNow
	}
	// Kept by a key without the proxies of the requests: made anew.
	p.agentProv, p.agentProvKey = nil, ""
	p.confSaid = nil
	res.Info = p.info()
	res.Switched = before.Profile != res.Info.Profile || before.Model != res.Info.Model || before.Effort != res.Info.Effort
	return res, nil
}

// shellModel is the profile, the model and the effort of the shell, and the
// profile config.toml selected for it as the proxy last read it.
type shellModel struct{ profile, model, effort, def string }

// target is where the shell goes with the config applied: the profile, as
// LoadProfile has it, with the provider for its models and levels of
// effort, and the model and the effort on it. gone: the shell's profile is
// not in config.toml any more, and leaveGone takes it there.
type target struct {
	cfg           config.Config
	prov          llm.Provider
	model, effort string
	gone          bool
}

// follow is where the shell sh goes with next, read anew from config.toml
// that old was read from; top is next's top level, def what next selects
// for the shell's environment (defErr if it selects none). The shell
// follows config.toml where it has what config.toml gave it, and keeps
// what `aish model` chose: on the profile config.toml selected it goes to
// the one selected now, with that one's model and effort; on a profile of
// its own it stays, and its model and effort follow an edit of that
// profile's only while they are the ones the profile had. A profile gone
// from config.toml sends it where leaveGone would; that it can go there is
// checked here, before anything is applied.
func (p *Proxy) follow(old, next *config.Snapshot, top, def config.Config, defErr error, sh shellModel) (target, error) {
	to := sh.profile
	_, kept := top.Profiles[to]
	gone := to != "" && !kept
	switch {
	case gone && defErr != nil:
		to = ""
	case gone || defErr == nil && sh.profile == sh.def:
		to = def.Profile
	}
	tries := []string{to}
	if gone && to != "" {
		tries = append(tries, "") // as leaveGone, the top level is always there
	}
	var t target
	var err error
	for _, name := range tries {
		if t.cfg, err = next.LoadProfile(name); err == nil {
			if t.prov, err = p.listProvider(t.cfg); err == nil {
				break
			}
		}
	}
	if err != nil {
		return target{}, err
	}
	t.gone = gone
	switch {
	case gone:
		t.model, t.effort = t.cfg.Model, t.cfg.Effort
	case t.cfg.Profile != sh.profile:
		// As aish would start in this shell: $AISH_MODEL and $AISH_EFFORT too.
		t.model, t.effort = def.Model, def.Effort
	default:
		was, _ := old.LoadProfile(sh.profile)
		t.model, t.effort = sh.model, sh.effort
		if t.model == was.Model {
			t.model = t.cfg.Model
		}
		if t.effort == was.Effort {
			t.effort = t.cfg.Effort
		}
	}
	if llm.CheckEffort(t.prov, t.effort) != nil {
		t.effort = ""
	}
	return t, nil
}

// restartKeys are the keys of config.toml that differ between was, the
// config aish started with, and now, and that only a restart applies: the
// bash it runs, the route its rc file reads once, the sessions it keeps
// and prunes as it starts.
func restartKeys(was, now config.Config) []string {
	var keys []string
	for _, k := range []struct {
		name   string
		differ bool
	}{
		{"shell", was.Shell != now.Shell},
		{"route", was.Route != now.Route},
		{"sessions_dir", was.SessionsDir != now.SessionsDir},
		{"sessions_ttl", was.SessionsTTL != now.SessionsTTL},
	} {
		if k.differ {
			keys = append(keys, k.name)
		}
	}
	return keys
}

// fileSum is the sha256 of the regular file at path, "" if there is none
// or it cannot be read. Not a FIFO, which would keep the read waiting.
func fileSum(path string) string {
	if st, err := os.Stat(path); err != nil || !st.Mode().IsRegular() {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// tellChanged says that the config files on disk differ from those in
// force: an edit waits for `aish apply-config`, and nothing else would tell
// why it changes nothing. Once for an edit of a file, not with every
// request until it is applied. Called under reqMu.
func (p *Proxy) tellChanged(conf *config.Snapshot, pols *policy.Cache, cwd string) {
	_, keys := pols.Changed()
	keys = append(keys, conf.Changed(cwd)...)
	if f, now, changed := p.mcpChanged(); changed {
		keys = append(keys, f+"\x00"+now)
	}
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

// mcpChanged tells whether the MCP config the servers are of differs on
// disk now from the file read, and what it reads now.
func (p *Proxy) mcpChanged() (file, now string, changed bool) {
	p.mu.Lock()
	file, sum := p.mcpFile, p.mcpSum
	p.mu.Unlock()
	if file == "" { // none read: a proxy Run has not started
		return "", "", false
	}
	now = fileSum(file)
	return file, now, now != sum
}

// unapplied names the config files on disk that differ from those in
// force, of conf and of the policies: what `aish apply-config` would apply.
func (p *Proxy) unapplied(conf *config.Snapshot) []string {
	names := conf.Stale()
	dirs, _ := p.policies.Changed()
	names = append(names, dirs...)
	if f, _, changed := p.mcpChanged(); changed {
		names = append(names, f)
	}
	return names
}
