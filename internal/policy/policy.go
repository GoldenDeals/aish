// Package policy decides whether the agent may run a tool call. The policies
// are Cedar files in policy_dir, validated against a built-in schema when
// loaded, and the deny/ask patterns of [policy] in config.toml; every simple
// command of a bash call is one authorization request, and the verdict is
// the strictest answer of every Checker. The engine is fail-closed: an
// evaluation error, a call no permit covers and a leftover Rego file are
// all a deny or a load error, never an allow.
package policy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
	// Commands holds the argv of every simple command in a bash tool call,
	// including those in pipelines, $(...), subshells and `bash -c` strings.
	Commands   [][]string `json:"commands,omitempty"`
	ParseError string     `json:"parse_error,omitempty"`
	// Model is the model making the call, the principal of the request.
	Model string `json:"model,omitempty"`
}

// Engine holds the checkers of a policy directory.
type Engine struct {
	checkers []Checker
}

// Load reads the *.cedar files of dir, a directory or a list of them in
// the form of PATH (the project's after the user's), and takes the rules
// of config.toml as one more checker. Every directory is a policy set and
// a checker of its own, so a call must pass each: in one set a permit
// overrides Cedar's default deny, and a cloned repository's
// `permit(principal, action, resource);` would undo every prohibition the
// user's set keeps by not permitting. A missing or empty dir and no rules
// give an engine that allows everything; a *.rego file is an error even
// next to Cedar files, because ignoring a file of prohibitions is not an
// option.
func Load(ctx context.Context, dir string, rules Rules) (*Engine, error) {
	if err := rules.check(); err != nil {
		return nil, err
	}
	e := &Engine{}
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

// NewInput fills the derived fields of the input for a tool call. Cwd is
// resolved as the paths are, so that `context.paths == [context.cwd]` holds
// for `cd .` in a directory reached through a symlink.
func NewInput(tool string, args map[string]any, cwd string) Input {
	home, _ := os.UserHomeDir()
	cwd = resolve(cwd)
	in := Input{Tool: tool, Args: args, Cwd: cwd, Home: resolve(home)}
	if p, ok := args["path"].(string); ok && p != "" {
		p = homePath(p, home)
		if !filepath.IsAbs(p) {
			p = filepath.Join(cwd, p)
		}
		in.Path = resolve(p)
	}
	if c, ok := args["command"].(string); ok && tool == "bash" {
		cmds, err := Commands(c)
		in.Commands = cmds
		if err != nil {
			in.ParseError = err.Error()
		}
	}
	return in
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
	p = filepath.Clean(p)
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
