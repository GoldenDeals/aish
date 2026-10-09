package main

// Tab after aish. The shells run `aish __complete WORD...`, the command
// line up to the word under the cursor, that word last, and aish prints
// what may stand there, a word a line; the shell keeps those that fit
// (bash by their start, zsh by its matchers). The scripts that ask are
// init.bash's and init.zsh's under aish, and outside it those `aish
// completion bash|zsh` prints (internal/shellinit/complete.*).

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/mcp"
	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/session"
	"github.com/GoldenDeals/aish/internal/shellinit"
	"github.com/GoldenDeals/aish/internal/skills"
	"github.com/GoldenDeals/aish/internal/subagent"
	"github.com/GoldenDeals/aish/internal/tools"
)

// compSpec is what Tab offers after a subcommand of aish. Name is the
// subcommand, with a word more for one of its own ("session rm"), which
// Tab offers where the subcommand takes its first argument. Flags are
// offered for a word that starts with -, or where nothing else fits; a
// flag that takes a value names it after = ("--agent={agent}"), a {kind}
// for one Tab completes, a word in capitals for one it does not
// ("--older=AGE"). Args are what the arguments take, in order: words and
// {kinds}, the last one again and again if it ends in "...".
type compSpec struct {
	name  string
	flags string
	args  []string
}

// compSpecs are the subcommands Tab knows, a line each: a new subcommand
// is a new line (TestCompSpecs fails without it), one of a subcommand
// "parent sub". The kinds are those of compKinds.
var compSpecs = []compSpec{
	{name: "", flags: "--resume --help"},
	{name: "agents"},
	{name: "apply-config"},
	{name: "clear"},
	{name: "compact"},
	{name: "completion", args: []string{"bash zsh"}},
	{name: "context", flags: "--full"},
	{name: "expand"},
	{name: "help"},
	{name: "hooks"},
	{name: "init", args: []string{"bash zsh"}},
	{name: "mcp"},
	{name: "model", args: []string{"{profile} {model} {effort}", "{model} {effort}", "{effort}"}},
	{name: "new"},
	{name: "policy", flags: "--agent={agent}", args: []string{"{anytool}"}},
	{name: "recap"},
	{name: "resume", flags: "--all", args: []string{"{named}"}},
	{name: "session"},
	{name: "session prune", flags: "--older=AGE"},
	{name: "session rename", args: []string{"{session}"}},
	{name: "session rm", args: []string{"{named}..."}},
	{name: "session show"},
	{name: "skills"},
	{name: "status"},
	{name: "tasks"},
	{name: "tasks show", args: []string{"{task}"}},
	{name: "tool", args: []string{"{tool}"}},
	{name: "trust", flags: "--revoke --list"},
	{name: "yolo", args: []string{"off on"}},
}

// compTimeout bounds what one Tab waits for the proxy, all of its calls
// together: a proxy that does not answer by then leaves the files on disk,
// or nothing.
const compTimeout = 500 * time.Millisecond

func specOf(name string) (compSpec, bool) {
	i := slices.IndexFunc(compSpecs, func(s compSpec) bool { return s.name == name })
	if i < 0 {
		return compSpec{}, false
	}
	return compSpecs[i], true
}

// child is the spec of word as a subcommand of s.
func (s compSpec) child(word string) (compSpec, bool) {
	return specOf(strings.TrimSpace(s.name + " " + word))
}

// children are the names of the subcommands of s.
func (s compSpec) children() []string {
	var out []string
	for _, c := range compSpecs {
		if parent, sub, ok := cutLast(c.name); ok && parent == s.name {
			out = append(out, sub)
		}
	}
	return out
}

// cutLast splits a spec's name before its last word; a top-level one has
// the root, "", for parent.
func cutLast(name string) (parent, last string, ok bool) {
	if name == "" {
		return "", "", false
	}
	if i := strings.LastIndexByte(name, ' '); i >= 0 {
		return name[:i], name[i+1:], true
	}
	return "", name, true
}

// flag tells whether s has the flag name and what its value is: "" for a
// flag without one.
func (s compSpec) flag(name string) (value string, ok bool) {
	for _, f := range strings.Fields(s.flags) {
		if n, v, _ := strings.Cut(f, "="); n == name {
			return v, true
		}
	}
	return "", false
}

func (s compSpec) flagNames() []string {
	var out []string
	for _, f := range strings.Fields(s.flags) {
		n, _, _ := strings.Cut(f, "=")
		out = append(out, n)
	}
	return out
}

// arg is what argument i takes.
func (s compSpec) arg(i int) (string, bool) {
	if len(s.args) == 0 {
		return "", false
	}
	if i < len(s.args) {
		return strings.TrimSuffix(s.args[i], "..."), true
	}
	if last := s.args[len(s.args)-1]; strings.HasSuffix(last, "...") {
		return strings.TrimSuffix(last, "..."), true
	}
	return "", false
}

func completeCmd(words []string) int {
	if len(words) < 2 {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), compTimeout)
	defer cancel()
	c := &completer{ctx: ctx, configs: map[string]*config.Config{}}
	c.cwd, _ = os.Getwd()
	if client, err := rpc.FromEnv(); err == nil {
		c.client = client
	}
	for _, w := range c.complete(words[1:len(words)-1], words[len(words)-1]) {
		fmt.Println(w)
	}
	return 0
}

// completer finds the values of the kinds, once each: from the proxy
// inside aish, else from the files.
type completer struct {
	ctx     context.Context
	client  *rpc.Client // nil outside aish
	cwd     string
	cur     string   // the word under the cursor, as far as typed
	flags   []string // those typed, of the subcommand
	info    *rpc.Info
	configs map[string]*config.Config // by profile, "-" the shell's
}

// complete is what may stand in cur after the words prev of aish.
func (c *completer) complete(prev []string, cur string) []string {
	spec, _ := specOf("")
	var args []string // of spec, so far
	waiting, value := false, ""
	c.cur, c.flags = cur, nil
	for _, w := range prev {
		switch {
		case waiting:
			// bash takes the = of --flag=value for a word of its own.
			if w != "=" {
				waiting = false
			}
		case w == "--":
			return nil
		case strings.HasPrefix(w, "-"):
			name, _, given := strings.Cut(w, "=")
			c.flags = append(c.flags, name)
			if v, ok := spec.flag(name); ok && v != "" && !given {
				waiting, value = true, v
			}
		case len(args) == 0:
			if sub, ok := spec.child(w); ok {
				spec, c.flags = sub, nil
				continue
			}
			if spec.name == "" {
				return nil // no such subcommand
			}
			args = append(args, w)
		default:
			args = append(args, w)
		}
	}
	if waiting {
		return c.words(value, args)
	}
	if strings.HasPrefix(cur, "-") {
		// zsh has the word whole, --flag=value: a value comes with its flag.
		if name, _, ok := strings.Cut(cur, "="); ok {
			v, _ := spec.flag(name)
			var out []string
			for _, w := range c.words(v, args) {
				out = append(out, name+"="+w)
			}
			return out
		}
		return spec.flagNames()
	}
	var out []string
	if len(args) == 0 {
		out = spec.children()
	}
	if a, ok := spec.arg(len(args)); ok {
		out = append(out, c.words(a, args)...)
	}
	if len(out) == 0 {
		return spec.flagNames()
	}
	return out
}

// words are those of what, words and {kinds}, but for those typed in args
// already.
func (c *completer) words(what string, args []string) []string {
	var out []string
	for _, f := range strings.Fields(what) {
		if kind, ok := strings.CutPrefix(f, "{"); ok {
			out = append(out, c.values(strings.TrimSuffix(kind, "}"), args)...)
		} else if f == strings.ToLower(f) {
			out = append(out, f) // a word; AGE stands for a value Tab does not know
		}
	}
	var fresh []string
	for _, w := range out {
		if !slices.Contains(args, w) && !slices.Contains(fresh, w) {
			fresh = append(fresh, w)
		}
	}
	return fresh
}

// compKinds are the values of each {kind}, after the arguments args: quick
// ones, from the files and the proxy. The models of a provider are a
// request to it: only those of the config come.
var compKinds = map[string]func(c *completer, args []string) []string{
	"agent": func(c *completer, _ []string) []string {
		var out []string
		found, _ := subagent.Find(c.cwd)
		for _, d := range found {
			out = append(out, d.Name)
		}
		return out
	},
	"anytool": func(c *completer, _ []string) []string { return c.tools(true) },
	"effort":  func(c *completer, args []string) []string { return c.efforts(c.profileIn(args)) },
	"model":   func(c *completer, args []string) []string { return c.models(c.profileIn(args)) },
	// The sessions aish resume lists, and session.Find takes: those the
	// user named, all of them with --all.
	"named": func(c *completer, _ []string) []string {
		all := slices.Contains(c.flags, "--all") || slices.Contains(c.flags, "-a")
		return c.sessions(!all, true)
	},
	"profile": func(c *completer, _ []string) []string { return c.profiles() },
	// Any session, as session.FindAll takes it: this shell's too.
	"session": func(c *completer, _ []string) []string { return c.sessions(false, false) },
	"task":    func(c *completer, _ []string) []string { return c.tasks() },
	"tool":    func(c *completer, _ []string) []string { return c.tools(false) },
}

func (c *completer) values(kind string, args []string) []string {
	if f, ok := compKinds[kind]; ok {
		return f(c, args)
	}
	return nil
}

// shell is what the proxy says of this shell, nil outside aish or when it
// does not answer.
func (c *completer) shell() *rpc.Info {
	if c.info == nil && c.client != nil {
		var info rpc.Info
		if c.client.CallContext(c.ctx, rpc.MethodInfo, nil, &info) == nil {
			c.info = &info
		}
	}
	return c.info
}

// config is the config of profile, nil for the shell's, in cwd: inside
// aish the one in force, else, or when the proxy does not answer, the
// files on disk.
func (c *completer) config(profile *string) (config.Config, bool) {
	key := "-"
	if profile != nil {
		key = *profile
	}
	if cfg, ok := c.configs[key]; ok {
		if cfg == nil {
			return config.Config{}, false
		}
		return *cfg, true
	}
	cfg, err := c.load(profile)
	if err != nil {
		c.configs[key] = nil
		return config.Config{}, false
	}
	c.configs[key] = &cfg
	return cfg, true
}

func (c *completer) load(profile *string) (config.Config, error) {
	if c.client != nil {
		var res rpc.Config
		err := c.client.CallContext(c.ctx, rpc.MethodConfig, rpc.ConfigParams{Cwd: c.cwd, Env: os.Environ(), Profile: profile}, &res)
		if err == nil {
			return res.Config, nil
		}
	}
	conf := config.NewSnapshot()
	disk, err := conf.LoadEnv(os.Getenv)
	if err != nil {
		if disk, err = conf.LoadProfile(""); err != nil {
			return config.Config{}, err
		}
	}
	a, err := onDisk(disk, c.cwd, profile, false)
	return a.cfg, err
}

// sessions are the names of the sessions on disk: the user's, else, but
// for named, the model's. Not the ids, more of them than of names, and
// long, but for those that start with the word typed. Not this shell's
// session if others: neither resume nor rm takes it.
func (c *completer) sessions(named, others bool) []string {
	dir, cur := "", ""
	if info := c.shell(); info != nil {
		dir, cur = info.Dir, info.SessionID
	}
	if dir == "" {
		cfg, ok := c.config(nil)
		if !ok {
			return nil
		}
		dir = cfg.SessionsDir
	}
	// Not session.List: it reads the end of every journal.
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	var out []string
	for _, f := range files {
		id := strings.TrimSuffix(filepath.Base(f), ".jsonl")
		if session.CheckID(id) != nil || others && id == cur {
			continue
		}
		name := readFirstLine(filepath.Join(dir, id+".name"))
		if name == "" && !named {
			name = readFirstLine(filepath.Join(dir, id+".title"))
		}
		if name != "" {
			out = append(out, name)
		}
		if c.cur != "" && strings.HasPrefix(id, c.cur) {
			out = append(out, id)
		}
	}
	return out
}

func readFirstLine(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(string(b), "\n")
	return strings.TrimSpace(line)
}

// profiles are those of config.toml, root among them, if it has any.
func (c *completer) profiles() []string {
	cfg, ok := c.config(nil)
	if !ok || len(cfg.Profiles) == 0 {
		return nil
	}
	return append([]string{config.Root}, cfg.ProfileNames()...)
}

// profileIn is the profile args of aish model name, nil for none.
func (c *completer) profileIn(args []string) *string {
	if len(args) == 0 {
		return nil
	}
	if args[0] == config.Root {
		return new(string)
	}
	if slices.Contains(c.profiles(), args[0]) {
		return &args[0]
	}
	return nil
}

// models are the model of the config, of profile or of the shell's, and
// the one the shell has switched to.
func (c *completer) models(profile *string) []string {
	var out []string
	if cfg, ok := c.config(profile); ok && cfg.Model != "" {
		out = append(out, cfg.Model)
	}
	if info := c.shell(); profile == nil && info != nil && info.Model != "" {
		out = append(out, info.Model)
	}
	return out
}

// efforts are the levels of the provider of profile, or of the shell's.
func (c *completer) efforts(profile *string) []string {
	out := []string{"default"}
	cfg, ok := c.config(profile)
	if !ok {
		return out
	}
	cfg.Effort = ""
	if p, err := llm.New(cfg); err == nil {
		out = append(out, p.Efforts()...)
	}
	return out
}

// tools are the names `aish tool` runs, or, with all, those `aish policy`
// asks about: the MCP tools the proxy knows already, without starting a
// server.
func (c *completer) tools(all bool) []string {
	cfg, _ := c.config(nil)
	reg := tools.Load(cfg.ToolsDir)
	found, _ := skills.Find(c.cwd)
	for _, s := range found {
		reg.Add(s.Tool())
	}
	var out []string
	for _, t := range reg.All() {
		if all || runnable(t) {
			out = append(out, t.Name())
		}
	}
	if c.client != nil {
		var res mcp.ListResult
		if c.client.CallContext(c.ctx, rpc.MethodMCPList, mcp.ListParams{}, &res) == nil {
			for _, t := range res.Tools {
				out = append(out, t.Name)
			}
		}
	}
	return out
}

// tasks are the ids of the subagents in the background.
func (c *completer) tasks() []string {
	if c.client == nil {
		return nil
	}
	var list []rpc.Task
	if c.client.CallContext(c.ctx, rpc.MethodTasks, nil, &list) != nil {
		return nil
	}
	var out []string
	for _, t := range list {
		out = append(out, t.ID)
	}
	return out
}

// completionCmd prints the Tab completion of aish for a shell not under
// aish, which has it already: the installer puts it where bash-completion
// or zsh's compinit finds it.
func completionCmd(args []string) int {
	switch {
	case len(args) == 1 && args[0] == "bash":
		fmt.Print(shellinit.CompleteBash)
	case len(args) == 1 && args[0] == "zsh":
		fmt.Print(shellinit.CompleteZsh)
	default:
		return fail(errors.New("usage: aish completion bash|zsh"))
	}
	return 0
}
