// Package policy decides whether the agent may run a tool call, using Rego
// policies. The query is data.aish.decision, which must evaluate to
// {"action": "allow"|"deny"|"ask", "reason": "..."}; undefined means allow.
package policy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/open-policy-agent/opa/v1/rego"
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

// Input is what policies see as `input`.
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
}

type Engine struct {
	q *rego.PreparedEvalQuery
}

// Load compiles every *.rego file in dir. A missing or empty dir gives an
// engine that allows everything.
func Load(ctx context.Context, dir string) (*Engine, error) {
	files, _ := filepath.Glob(filepath.Join(dir, "*.rego"))
	if len(files) == 0 {
		return &Engine{}, nil
	}
	opts := []func(*rego.Rego){rego.Query("data.aish.decision")}
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		opts = append(opts, rego.Module(f, string(src)))
	}
	q, err := rego.New(opts...).PrepareForEval(ctx)
	if err != nil {
		return nil, fmt.Errorf("policy: %w", err)
	}
	return &Engine{q: &q}, nil
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

func (e *Engine) Check(ctx context.Context, in Input) (Decision, error) {
	if e == nil || e.q == nil {
		return Decision{Action: Allow}, nil
	}
	rs, err := e.q.Eval(ctx, rego.EvalInput(in))
	if err != nil {
		return Decision{}, fmt.Errorf("policy: %w", err)
	}
	if len(rs) == 0 || len(rs[0].Expressions) == 0 {
		return Decision{Action: Allow}, nil
	}
	m, ok := rs[0].Expressions[0].Value.(map[string]any)
	if !ok {
		return Decision{}, fmt.Errorf("policy: data.aish.decision is %T, want an object", rs[0].Expressions[0].Value)
	}
	d := Decision{}
	d.Action, _ = m["action"].(string)
	d.Reason, _ = m["reason"].(string)
	switch d.Action {
	case Allow, Deny, Ask:
	case "":
		d.Action = Allow
	default:
		return Decision{}, fmt.Errorf("policy: unknown action %q", d.Action)
	}
	return d, nil
}
