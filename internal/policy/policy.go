// Package policy decides whether the agent may run a tool call. The policies
// are Cedar files in policy_dir, validated against a built-in schema when
// loaded, and the deny/ask patterns of [policy] in config.toml; every simple
// command a call hands to the shell is one authorization request, and the
// verdict is the strictest answer of every Checker. The engine is
// fail-closed: an evaluation error, a call no permit covers and a leftover
// Rego file are all a deny or a load error, never an allow.
package policy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

const (
	Allow = "allow"
	Deny  = "deny"
	Ask   = "ask"
)

type Decision struct {
	Action string
	Reason string
}

// Input is what the policies see of a tool call.
type Input struct {
	Tool string `json:"tool"`
	// Server is the MCP server providing the tool, if any.
	Server string         `json:"server,omitempty"`
	Args   map[string]any `json:"args"`
	Cwd    string         `json:"cwd"`
	Home   string         `json:"home"`
	// Path is args.path resolved against Cwd, for file tools.
	Path string `json:"path,omitempty"`
	// Line is the command the call hands to the user's shell (bash, or any
	// tool doing so); "" when it hands none.
	Line string `json:"line,omitempty"`
	// Commands holds the argv of every simple command in Line, including
	// those in pipelines, $(...), subshells and `bash -c` strings.
	Commands   [][]string `json:"commands,omitempty"`
	ParseError string     `json:"parse_error,omitempty"`
	// Dynamic is Script.Dynamic of Line: what in it runs code no policy
	// has seen; a cd to where no policy can follow is "computed" too.
	Dynamic []string `json:"dynamic,omitempty"`
	// Writes is Script.Writes of Line: the files its redirections write.
	Writes []string `json:"writes,omitempty"`
	// UnknownWrite is Script.UnknownWrite of Line: a redirection in it
	// writes a file known only at run time.
	UnknownWrite bool `json:"-"`
	// Remote is Script.Remote of Line: the commands of Commands that run
	// on another machine.
	Remote []int `json:"-"`
	// Model is the model making the call, the principal of the request.
	Model string `json:"model,omitempty"`
	// Agent is the subagent making the call; "" for the host agent.
	Agent string `json:"agent,omitempty"`
	// sites is Script.sites of Line: where it runs each of Commands.
	sites []site
	// sh is the shell the call is made in, as NewInput finds it in its
	// environment: the commands of Line take ~ and $HOME from its HOME,
	// not from Home, and relative paths from its logical directory.
	sh shell
}

// Engine holds the checkers of a policy directory.
type Engine struct {
	checkers []Checker
	// agent names the subagent whose calls this engine judges: see
	// Subagent.
	agent string
}

// Subagent is the engine judging the calls of subagent name: the same
// checkers, with Input.Agent set to name in every call they see. The
// subagent's input is built by the same code as the host's, which knows
// nothing of subagents, so its name is put in here, where no check can
// miss it, a second one after a hook replaced the arguments included.
func (e *Engine) Subagent(name string) *Engine {
	if e == nil {
		return nil
	}
	sub := *e
	sub.agent = name
	return &sub
}

// Load reads the *.cedar files of dir, a directory or a list of them in
// the form of PATH (the project's after the user's), and takes the rules
// of config.toml as one more checker. Every directory is a policy set and
// a checker of its own, so a call must pass each: in one set a permit
// overrides Cedar's default deny, and a cloned repository's
// `permit(principal, action, resource);` would undo every prohibition the
// user's set keeps by not permitting. A missing or empty dir and no rules
// give an engine that allows everything but trusting a project (the
// guard, in every engine); a *.rego file is an error even next to Cedar
// files, because ignoring a file of prohibitions is not an option.
func Load(ctx context.Context, dir string, rules Rules) (*Engine, error) {
	if err := rules.check(); err != nil {
		return nil, err
	}
	e := &Engine{checkers: []Checker{guardChecker{}}}
	if rules.Len() > 0 {
		e.checkers = append(e.checkers, rulesChecker{rules})
	}
	for _, d := range filepath.SplitList(dir) {
		if rego, _ := filepath.Glob(filepath.Join(d, "*.rego")); len(rego) > 0 {
			return nil, fmt.Errorf("policy: %s: Rego policies are not supported anymore, rewrite it in Cedar (see README, «Политики»)", rego[0])
		}
		files, _ := filepath.Glob(filepath.Join(d, "*.cedar"))
		if len(files) == 0 {
			continue
		}
		c, err := loadCedar(files)
		if err != nil {
			return nil, err
		}
		e.checkers = append(e.checkers, c)
	}
	return e, nil
}

// NewInput fills the derived fields of the input for a tool call made from
// cwd in a shell whose environment is env (nil: the process's own, as for
// tools.Exec) and whose options on are opts, by name, as far as they are
// known (tools.Exec.Opts): the line the call hands to it runs in the modes
// they hold from its start, as it does in a bash that started with them in
// SHELLOPTS and BASHOPTS of env. Cwd is resolved as the paths are, so that
// `context.paths == [context.cwd]` holds for `cd .` in a directory reached
// through a symlink. A file tool's relative path is taken from cwd as
// given, as the tool takes it: from a link, ../x is next to the link.
func NewInput(tool string, args map[string]any, cwd string, env []string, opts ...string) Input {
	home, _ := os.UserHomeDir()
	in := Input{Tool: tool, Args: args, Cwd: resolve(cwd), Home: resolve(home)}
	in.sh = shell{
		pwd:    logical(cwd, getenv(env, "PWD"), in.Cwd),
		home:   getenv(env, "HOME"),
		cdpath: getenv(env, "CDPATH"),
		path:   getenv(env, "PATH"),
		login:  getenv(env, "SHELL"),
		modes:  shellModes(env, opts),
		env:    true,
	}
	if p, ok := args["path"].(string); ok && p != "" {
		p = homePath(p, home)
		if !filepath.IsAbs(p) {
			p = filepath.Join(cwd, p)
		}
		in.Path = resolve(p)
	}
	return in
}

// getenv is the value of name in env, or in the process's environment when
// env is nil, as tools.Exec.Getenv has it.
func getenv(env []string, name string) string {
	if env == nil {
		return os.Getenv(name)
	}
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == name {
			return v
		}
	}
	return ""
}

// logical is the shell's name for the directory cwd, whose resolved path
// is real: PWD when it names that directory, as bash takes PWD from its
// environment and os.Getwd does, else cwd as given.
func logical(cwd, pwd, real string) string {
	if filepath.IsAbs(pwd) && resolve(pwd) == real {
		return filepath.Clean(pwd)
	}
	if cwd == "" {
		return ""
	}
	return filepath.Clean(cwd)
}

// HandOff marks the call as one that hands line to the user's shell, so
// that the policies judge its commands one by one and the files its
// redirections write, in the shell of the call: NewInput's, or Cwd and
// Home of an input made otherwise.
func (in *Input) HandOff(line string) {
	in.Line = line
	in.sh.quoted = quotedPrefix(line)
	sh := in.shell()
	s, err := parseIn(line, sh)
	in.Commands, in.Dynamic, in.Writes = s.Commands, s.Dynamic, s.Writes
	in.Remote, in.sites = s.Remote, s.sites
	in.UnknownWrite = s.UnknownWrite
	if err != nil {
		in.ParseError = err.Error()
	}
	// A cd to where no policy can follow is as unknown as a command built
	// at run time, and so is all that runs there after it. A cd over ssh
	// moves a shell of another machine.
	for i, argv := range in.Commands {
		if slices.Contains(in.Remote, i) {
			continue
		}
		if sh.analyze(argv).lost && !slices.Contains(in.Dynamic, dynComputed) {
			in.Dynamic = append(in.Dynamic, dynComputed)
			sort.Strings(in.Dynamic)
		}
	}
}

// Analyze is Analyze of argv in the shell the call is made in: from its
// logical directory, with its HOME and CDPATH. A command of Line may run
// elsewhere, after a cd: the policies take its paths from there (see
// command).
func (in Input) Analyze(argv []string) Command {
	return in.shell().analyze(argv)
}

// shell is in.sh, or Cwd and Home for an input NewInput did not make.
func (in Input) shell() shell {
	if in.sh.env {
		return in.sh
	}
	return shell{pwd: in.Cwd, home: in.Home, quoted: in.sh.quoted}
}

// maxLinks is MAXSYMLINKS of Linux: after as many links on one path the
// kernel gives up with ELOOP.
const maxLinks = 40

// resolve follows the symlinks in p, so that ~/link → /etc does not pass for
// a path in $HOME. It walks p name by name, as the kernel does: a dangling
// link is followed to its target, which a write through it would create,
// and a ".." in a link's target goes up from where the link really is. The
// part of p that does not exist yet (a file to be written) is kept as is on
// top of its nearest existing parent. On a loop of links it gives up, as
// the kernel does, with the target of the last link read: a write there
// fails with ELOOP.
func resolve(p string) string {
	if p == "" {
		return p
	}
	return walk(filepath.Clean(p))
}

// walk is resolve of p as spelled. resolve cleans p first, because the file
// tools clean their path before they open it and write link/../x as x; a
// command hands its path to the kernel uncleaned, and there the ".." goes
// up from where link really leads. A relative p is returned as is.
func walk(p string) string {
	if !filepath.IsAbs(p) {
		return p
	}
	done, todo := "/", p
	for links := 0; todo != ""; {
		var name string
		name, todo, _ = strings.Cut(todo, "/")
		switch name {
		case "", ".":
			continue
		case "..":
			done = filepath.Dir(done)
			continue
		}
		next := filepath.Join(done, name)
		st, err := os.Lstat(next)
		if err != nil {
			// Nothing exists below a missing name: the rest is created as
			// it is spelled.
			return filepath.Join(next, todo)
		}
		if st.Mode()&os.ModeSymlink == 0 {
			done = next
			continue
		}
		target, err := os.Readlink(next)
		if err != nil {
			return filepath.Join(next, todo)
		}
		if filepath.IsAbs(target) {
			done = "/"
		}
		if links++; links > maxLinks {
			return filepath.Join(done, target, todo)
		}
		todo = target + "/" + todo
	}
	return done
}

// homePath expands a leading ~ of a file tool's path, before it is taken
// as relative to cwd. The home is os.UserHomeDir, as in the builtin tools'
// Execute, and not HOME of the shell: the policy must see the path the
// tool writes.
func homePath(p, home string) string {
	if home == "" {
		return p
	}
	if rest, ok := strings.CutPrefix(p, "~"); ok && (rest == "" || strings.HasPrefix(rest, "/")) {
		return filepath.Join(home, rest)
	}
	return p
}

// Check asks every checker and returns the strictest verdict.
func (e *Engine) Check(ctx context.Context, in Input) (Decision, error) {
	if e == nil || len(e.checkers) == 0 {
		return Decision{Action: Allow}, nil
	}
	if e.agent != "" {
		in.Agent = e.agent
	}
	ds := make([]Decision, 0, len(e.checkers))
	for _, c := range e.checkers {
		d, err := c.Check(ctx, in)
		if err != nil {
			return Decision{}, fmt.Errorf("policy: %w", err)
		}
		ds = append(ds, d)
	}
	return combine(ds), nil
}

// Summary lists the loaded policy files with their policy counts, for
// `aish policy`.
func (e *Engine) Summary() []Summary {
	var out []Summary
	if e == nil {
		return out
	}
	for _, c := range e.checkers {
		if s, ok := c.(interface{ Summaries() []Summary }); ok {
			out = append(out, s.Summaries()...)
		}
	}
	return out
}
