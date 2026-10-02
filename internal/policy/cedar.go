package policy

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	cedar "github.com/cedar-policy/cedar-go"
	"github.com/cedar-policy/cedar-go/types"
	xast "github.com/cedar-policy/cedar-go/x/exp/ast"
	"github.com/cedar-policy/cedar-go/x/exp/schema"
	"github.com/cedar-policy/cedar-go/x/exp/schema/validate"
)

// schemaSrc is what a policy may refer to. Validating every policy against
// it at load time is the point of Cedar here: a misspelled attribute is an
// error the user sees at once, not a rule that silently never fires.
//
//go:embed schema.cedarschema
var schemaSrc []byte

// Summary is one policy file as loaded.
type Summary struct {
	File     string // basename
	Policies int
}

type cedarChecker struct {
	ps      *cedar.PolicySet
	files   []string
	summary []Summary
}

// loadCedar parses the files into one policy set, naming the policies
// <basename>:<n> so that two files do not collide on policy0, and
// validates each against the schema.
func loadCedar(files []string) (*cedarChecker, error) {
	var sch schema.Schema
	if err := sch.UnmarshalCedar(schemaSrc); err != nil {
		return nil, fmt.Errorf("policy: built-in schema: %w", err)
	}
	resolved, err := sch.Resolve()
	if err != nil {
		return nil, fmt.Errorf("policy: built-in schema: %w", err)
	}
	v := validate.New(resolved)
	attrs := contextAttrs(resolved)
	c := &cedarChecker{ps: cedar.NewPolicySet(), files: files}
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("policy: %w", err)
		}
		list, err := cedar.NewPolicyListFromBytes(f, src)
		if err != nil {
			return nil, fmt.Errorf("policy: %w", err)
		}
		base := filepath.Base(f)
		for i, p := range list {
			id := fmt.Sprintf("%s:%d", base, i)
			// The validator's message names the policy ID itself.
			ast := (*xast.Policy)(p.AST())
			if err := v.Policy(id, ast); err != nil {
				return nil, fmt.Errorf("policy: %s:%d: %w", f, p.Position().Line, err)
			}
			if err := checkHas(ast, attrs); err != nil {
				return nil, fmt.Errorf("policy: %s:%d: %w", f, p.Position().Line, err)
			}
			c.ps.Add(types.PolicyID(id), p)
		}
		c.summary = append(c.summary, Summary{File: base, Policies: len(list)})
	}
	return c, nil
}

func (c *cedarChecker) Summaries() []Summary { return c.summary }

func (c *cedarChecker) Check(ctx context.Context, in Input) (Decision, error) {
	reqs, ents := requests(in)
	ds := make([]Decision, 0, len(reqs))
	for _, r := range reqs {
		if err := ctx.Err(); err != nil {
			return Decision{}, err
		}
		ds = append(ds, c.decide(r, ents))
	}
	return combine(ds), nil
}

// decide turns one authorization into a verdict. Cedar is default deny and
// an evaluation error only drops the failing policy, so both cases are made
// explicit here: no permit is a deny with a reason, and any error is a deny
// whatever the decision was.
func (c *cedarChecker) decide(req types.Request, ents types.EntityMap) Decision {
	dec, diag := cedar.Authorize(c.ps, ents, req)
	if len(diag.Errors) > 0 {
		var reasons []string
		for _, e := range diag.Errors {
			reasons = append(reasons, fmt.Sprintf("%s: %s", e.PolicyID, e.Message))
		}
		return Decision{Action: Deny, Reason: strings.Join(reasons, "; ")}
	}
	if dec == cedar.Allow {
		return Decision{Action: Allow}
	}
	if len(diag.Reasons) == 0 {
		return Decision{Action: Deny, Reason: fmt.Sprintf("no permit for %s %s", req.Action.ID, req.Resource)}
	}
	sort.Slice(diag.Reasons, func(i, j int) bool {
		a, b := diag.Reasons[i].Position, diag.Reasons[j].Position
		if a.Filename != b.Filename {
			return a.Filename < b.Filename
		}
		return a.Line < b.Line
	})
	// A forbid that asks loses to one that denies: the question would be
	// moot, so its text is dropped from the reason.
	var asks, denies []string
	for _, r := range diag.Reasons {
		ann := c.ps.Get(r.PolicyID).Annotations()
		switch text, ok := ann["ask"]; {
		case ok:
			asks = append(asks, string(text))
		case ann["reason"] != "":
			denies = append(denies, string(ann["reason"]))
		default:
			denies = append(denies, fmt.Sprintf("%s:%d forbid", filepath.Base(r.Position.Filename), r.Position.Line))
		}
	}
	if len(denies) > 0 {
		return Decision{Action: Deny, Reason: strings.Join(denies, "; ")}
	}
	return Decision{Action: Ask, Reason: strings.Join(asks, "; ")}
}

var (
	actionRun   = types.NewEntityUID("Action", "run")
	actionRead  = types.NewEntityUID("Action", "read")
	actionWrite = types.NewEntityUID("Action", "write")
	actionCall  = types.NewEntityUID("Action", "call")
)

// requests maps a tool call to Cedar: one request per simple command of a
// call handing a command to the shell, one for a file tool, one for
// anything else; the entities are shared by all of them. The tool stays in
// the context of a command, so a policy can tell ssh from bash.
func requests(in Input) ([]types.Request, types.EntityMap) {
	principal := types.NewEntityUID("Model", types.String(in.Model))
	ents := types.EntityMap{}
	cwd, home := types.String(in.Cwd), types.String(in.Home)
	switch {
	case in.Line != "":
		var reqs []types.Request
		run := func(argv []string) {
			c := Analyze(argv, in.Cwd, in.Home)
			ctx := types.RecordMap{
				"tool":     types.String(in.Tool),
				"program":  types.String(c.Program),
				"args":     stringSet(c.Args),
				"flags":    stringSet(c.Flags),
				"operands": stringSet(c.Operands),
				"paths":    stringSet(c.Paths),
				"text":     types.String(c.Text),
				"line":     types.String(in.Line),
				"cwd":      cwd,
				"home":     home,
			}
			if in.ParseError != "" {
				ctx["parse_error"] = types.String(in.ParseError)
			}
			reqs = append(reqs, types.Request{
				Principal: principal,
				Action:    actionRun,
				Resource:  types.NewEntityUID("Command", types.String(c.Program)),
				Context:   types.NewRecord(ctx),
			})
		}
		for _, argv := range in.Commands {
			run(argv)
		}
		if len(in.Commands) == 0 || in.ParseError != "" {
			run(nil)
		}
		return reqs, ents
	case in.Tool == "read_file", in.Tool == "write_file", in.Tool == "edit_file":
		action := actionWrite
		if in.Tool == "read_file" {
			action = actionRead
		}
		file := types.NewEntityUID("File", types.String(in.Path))
		ents[file] = types.Entity{UID: file, Parents: dirs(ents, in.Path, in.Cwd, in.Home), Tags: tags(in.Args)}
		_, err := os.Stat(in.Path)
		return []types.Request{{
			Principal: principal,
			Action:    action,
			Resource:  file,
			Context: types.NewRecord(types.RecordMap{
				"tool":   types.String(in.Tool),
				"path":   types.String(in.Path),
				"exists": types.Boolean(err == nil),
				"cwd":    cwd,
				"home":   home,
			}),
		}}, ents
	}
	tool := types.NewEntityUID("Tool", types.String(in.Tool))
	ent := types.Entity{UID: tool, Tags: tags(in.Args)}
	ctx := types.RecordMap{"tool": types.String(in.Tool), "cwd": cwd, "home": home}
	if in.Server != "" {
		ent.Parents = types.NewEntityUIDSet(types.NewEntityUID("Server", types.String(in.Server)))
		ctx["server"] = types.String(in.Server)
	}
	if in.Path != "" {
		ctx["path"] = types.String(in.Path)
	}
	ents[tool] = ent
	return []types.Request{{Principal: principal, Action: actionCall, Resource: tool, Context: types.NewRecord(ctx)}}, ents
}

// dirs adds the directory chain of path to ents and returns the parents of
// the entity at path: its directory, plus the aliases Dir::"~" for home and
// Dir::"." for cwd when path is one of them, so that a policy can say
// `resource in Dir::"~"` without knowing the machine.
func dirs(ents types.EntityMap, path, cwd, home string) types.EntityUIDSet {
	cwd = resolve(cwd)
	parents := func(p string) types.EntityUIDSet {
		var set []types.EntityUID
		if parent := filepath.Dir(p); parent != p {
			set = append(set, types.NewEntityUID("Dir", types.String(parent)))
		}
		if p == home {
			set = append(set, types.NewEntityUID("Dir", "~"))
		}
		if p == cwd {
			set = append(set, types.NewEntityUID("Dir", "."))
		}
		return types.NewEntityUIDSet(set...)
	}
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		uid := types.NewEntityUID("Dir", types.String(dir))
		ents[uid] = types.Entity{UID: uid, Parents: parents(dir)}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	return parents(path)
}

// tags gives the raw tool arguments to a policy as strings: Cedar records
// are closed, so the arguments of an arbitrary tool cannot be typed in the
// schema, while tags may hold any key.
func tags(args map[string]any) types.Record {
	m := types.RecordMap{}
	for k, v := range args {
		if s, ok := v.(string); ok {
			m[types.String(k)] = types.String(s)
			continue
		}
		b, err := json.Marshal(v)
		if err != nil {
			b = []byte(fmt.Sprint(v))
		}
		m[types.String(k)] = types.String(b)
	}
	return types.NewRecord(m)
}

func stringSet(ss []string) types.Set {
	vs := make([]types.Value, len(ss))
	for i, s := range ss {
		vs[i] = types.String(s)
	}
	return types.NewSet(vs...)
}
