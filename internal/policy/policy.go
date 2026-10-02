// Package policy decides whether the agent may run a tool call. The policies
// are Cedar files in policy_dir, validated against a built-in schema when
// loaded; every simple command of a bash call is one authorization request,
// and the verdict is the strictest answer of every Checker. The engine is
// fail-closed: an evaluation error, a call no permit covers and a leftover
// Rego file are all a deny or a load error, never an allow.
package policy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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

// Load reads every *.cedar file in dir, a directory or a list of them in
// the form of PATH (the project's after the user's). A missing or empty
// dir gives an engine that allows everything; a *.rego file is an error
// even next to Cedar files, because ignoring a file of prohibitions is not
// an option.
func Load(ctx context.Context, dir string) (*Engine, error) {
	var files []string
	for _, d := range filepath.SplitList(dir) {
		if rego, _ := filepath.Glob(filepath.Join(d, "*.rego")); len(rego) > 0 {
			return nil, fmt.Errorf("policy: %s: Rego policies are not supported anymore, rewrite it in Cedar (see README, «Политики»)", rego[0])
		}
		fs, _ := filepath.Glob(filepath.Join(d, "*.cedar"))
		files = append(files, fs...)
	}
	if len(files) == 0 {
		return &Engine{}, nil
	}
	c, err := loadCedar(files)
	if err != nil {
		return nil, err
	}
	return &Engine{checkers: []Checker{c}}, nil
}

// NewInput fills the derived fields of the input for a tool call.
func NewInput(tool string, args map[string]any, cwd string) Input {
	home, _ := os.UserHomeDir()
	in := Input{Tool: tool, Args: args, Cwd: cwd, Home: resolve(home)}
	if p, ok := args["path"].(string); ok && p != "" {
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

// resolve follows the symlinks in p, so that ~/link → /etc does not pass for
// a path in $HOME. The part of p that does not exist yet (a file to be
// written) is kept as is on top of its nearest existing parent.
func resolve(p string) string {
	if p == "" {
		return p
	}
	p = filepath.Clean(p)
	var rest []string
	for dir := p; ; dir = filepath.Dir(dir) {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			slices.Reverse(rest)
			return filepath.Join(append([]string{real}, rest...)...)
		}
		if parent := filepath.Dir(dir); parent == dir {
			return p
		}
		rest = append(rest, filepath.Base(dir))
	}
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
