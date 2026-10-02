// Package tools defines what the agent can do. Every tool is also a command
// the user can type: built-ins via `aish tool <name>` wrappers on PATH,
// user tools are plain executables in the tools directory.
package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Bash is special: it runs in the user's live shell, driven by the agent.
const Bash = "bash"

type Arg struct {
	Name string
	// Type is string, integer, number, boolean, or object/array/any, which
	// the CLI form takes as JSON.
	Type     string
	Desc     string
	Required bool
	// Stdin marks the argument passed on standard input in the CLI form
	// (file contents, long text).
	Stdin bool
	// Flag marks an argument given as --name VALUE (a boolean as --name)
	// rather than by position.
	Flag bool
	// Rest takes all the remaining positional arguments of the CLI form,
	// joined so that splitting them as a shell does gives them back.
	Rest bool
}

type Tool struct {
	Name string
	Desc string
	Args []Arg
	// Run executes a built-in tool. Nil for bash and external tools.
	Run func(ctx context.Context, args map[string]any) (string, error)
	// Path of an external tool's executable.
	Path string

	// Server is the MCP server that provides the tool.
	Server string
	// RawSchema is the input schema as the tool's provider gave it.
	RawSchema json.RawMessage
	// Hidden tools are not offered to the model as tools; it runs them as
	// commands, which keeps their schemas out of every request.
	Hidden bool
}

// Schema is the JSON schema of the tool's input.
func (t Tool) Schema() map[string]any {
	if t.RawSchema != nil {
		var m map[string]any
		if json.Unmarshal(t.RawSchema, &m) == nil {
			return m
		}
	}
	props := map[string]any{}
	required := []string{}
	for _, a := range t.Args {
		typ := a.Type
		if typ == "" {
			typ = "string"
		}
		props[a.Name] = map[string]any{"type": typ, "description": a.Desc}
		if a.Required {
			required = append(required, a.Name)
		}
	}
	return map[string]any{"type": "object", "properties": props, "required": required}
}

// Registry holds the available tools in a stable order.
type Registry struct {
	list []Tool
	byID map[string]Tool
}

// Add registers t unless a tool with its name exists.
func (r *Registry) Add(t Tool) bool {
	if _, dup := r.byID[t.Name]; dup {
		return false
	}
	r.list = append(r.list, t)
	r.byID[t.Name] = t
	return true
}

func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.byID[name]
	return t, ok
}

func (r *Registry) All() []Tool { return r.list }

// Load returns the built-in tools plus executables found in dir: a
// directory, or a list of them in the form of PATH, the first with a
// name keeping it (the user's tools come before the project's).
func Load(dir string) *Registry {
	r := &Registry{byID: map[string]Tool{}}
	for _, t := range Builtins() {
		r.Add(t)
	}
	for _, d := range filepath.SplitList(dir) {
		for _, t := range loadExternal(d) {
			r.Add(t)
		}
	}
	return r
}

// Exec is where a tool runs: the shell's working directory, which relative
// paths are taken from, and its environment. The agent lives in the proxy,
// whose own are not the user's. The zero value is the process's own.
type Exec struct {
	Dir string
	Env []string
}

// Getenv is the value of name in Env, or in the process's environment when
// Env is nil.
func (e Exec) Getenv(name string) string {
	if e.Env == nil {
		return os.Getenv(name)
	}
	for _, kv := range e.Env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == name {
			return v
		}
	}
	return ""
}

// Execute runs a non-bash tool in this process's directory and environment
// and returns its textual result.
func (t Tool) Execute(ctx context.Context, args map[string]any, out io.Writer) (string, error) {
	return t.ExecuteIn(ctx, Exec{}, args, out)
}

// ExecuteIn is Execute in ex: a built-in's relative path is taken from
// ex.Dir, an external tool runs there with ex.Env.
func (t Tool) ExecuteIn(ctx context.Context, ex Exec, args map[string]any, out io.Writer) (string, error) {
	if t.Run != nil {
		if p, ok := args["path"].(string); ok && ex.Dir != "" && t.Server == "" && p != "" && !filepath.IsAbs(p) {
			args = maps.Clone(args)
			args["path"] = filepath.Join(ex.Dir, p)
		}
		return t.Run(ctx, args)
	}
	if t.Path != "" {
		return runExternal(ctx, t, ex, args, out)
	}
	return "", fmt.Errorf("tool %s cannot be executed directly", t.Name)
}

// ParseCLI maps command-line arguments to tool arguments: positional ones in
// declaration order, a Rest one taking what is left, flags as --name VALUE or
// --name=VALUE; a Stdin argument that is not given is read from stdin.
func (t Tool) ParseCLI(argv []string, stdin io.Reader) (map[string]any, error) {
	args := map[string]any{}
	usage := fmt.Errorf("usage: %s", t.Usage())
	flags := map[string]Arg{}
	for _, a := range t.Args {
		if a.Flag {
			flags[a.Name] = a
		}
	}
	var pos []string
	for i := 0; i < len(argv); i++ {
		s := argv[i]
		if len(flags) > 0 && s == "--" {
			pos = append(pos, argv[i+1:]...)
			break
		}
		name, val, hasVal := strings.Cut(strings.TrimPrefix(s, "--"), "=")
		a, ok := flags[name]
		if !ok || !strings.HasPrefix(s, "--") {
			pos = append(pos, s)
			continue
		}
		if !hasVal {
			if a.Type == "boolean" {
				val = "true"
			} else if i++; i < len(argv) {
				val = argv[i]
			} else {
				return nil, usage
			}
		}
		v, err := convert(a, val)
		if err != nil {
			return nil, fmt.Errorf("--%s: %w", a.Name, err)
		}
		args[a.Name] = v
	}
	n := 0
	for _, a := range t.Args {
		if a.Flag {
			if _, ok := args[a.Name]; !ok && a.Required {
				return nil, usage
			}
			continue
		}
		var raw string
		switch {
		case a.Rest && n < len(pos):
			raw, n = shellJoin(pos[n:]), len(pos)
		case n < len(pos):
			raw = pos[n]
			n++
		case a.Stdin:
			b, err := io.ReadAll(stdin)
			if err != nil {
				return nil, err
			}
			raw = string(b)
		case a.Required:
			return nil, usage
		default:
			continue
		}
		v, err := convert(a, raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", strings.ToUpper(a.Name), err)
		}
		args[a.Name] = v
	}
	if n < len(pos) {
		return nil, usage
	}
	return args, nil
}

func (t Tool) Usage() string {
	var b strings.Builder
	b.WriteString(t.Name)
	for _, a := range t.Args {
		n := strings.ToUpper(a.Name)
		switch {
		case a.Flag && a.Type == "boolean":
			n = "--" + a.Name
		case a.Flag && isJSON(a.Type):
			n = "--" + a.Name + " JSON"
		case a.Flag:
			n = "--" + a.Name + " " + n
		case a.Stdin:
			n += "|-"
		case a.Rest:
			n += "..."
		}
		if a.Required {
			fmt.Fprintf(&b, " %s", n)
		} else {
			fmt.Fprintf(&b, " [%s]", n)
		}
	}
	return b.String()
}

// shellJoin quotes the words that have blanks, quotes or backslashes.
func shellJoin(words []string) string {
	out := make([]string, len(words))
	for i, w := range words {
		out[i] = w
		if w == "" || strings.ContainsAny(w, " \t\n'\"\\") {
			out[i] = "'" + strings.ReplaceAll(w, "'", `'\''`) + "'"
		}
	}
	return strings.Join(out, " ")
}

func isJSON(typ string) bool { return typ == "object" || typ == "array" || typ == "any" }

func convert(a Arg, raw string) (any, error) {
	switch a.Type {
	case "integer":
		return strconv.Atoi(raw)
	case "number":
		return strconv.ParseFloat(raw, 64)
	case "boolean":
		return strconv.ParseBool(raw)
	case "object", "array":
		var v any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return nil, fmt.Errorf("invalid JSON: %w", err)
		}
		return v, nil
	case "any":
		// Untyped: JSON if it parses, a string otherwise.
		var v any
		if json.Unmarshal([]byte(raw), &v) == nil {
			return v, nil
		}
	}
	return raw, nil
}

// cliValue is the inverse of convert: a string as is, anything else as JSON.
func cliValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// Decode parses LLM-provided JSON arguments.
func Decode(raw json.RawMessage) (map[string]any, error) {
	args := map[string]any{}
	if len(raw) == 0 {
		return args, nil
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("invalid tool arguments: %w", err)
	}
	return args, nil
}

func str(args map[string]any, k string) string {
	s, _ := args[k].(string)
	return s
}

func num(args map[string]any, k string) int {
	switch v := args[k].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		n, _ := strconv.Atoi(v)
		return n
	}
	return 0
}

func boolean(args map[string]any, k string) bool {
	switch v := args[k].(type) {
	case bool:
		return v
	case string:
		b, _ := strconv.ParseBool(v)
		return b
	}
	return false
}

// loadExternal reads tool metadata from header comments of executables:
//
//	# aish:desc Search the issue tracker
//	# aish:arg query string Text to search for
//	# aish:arg limit? integer Maximum number of results
//	# aish:arg body stdin Text passed on standard input
func loadExternal(dir string) []Tool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Tool
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		st, err := os.Stat(path)
		if err != nil || st.IsDir() || st.Mode()&0o111 == 0 {
			continue
		}
		t := Tool{Name: e.Name(), Path: path}
		if !parseHeader(path, &t) {
			continue
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func parseHeader(path string, t *Tool) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	found := false
	for n := 0; sc.Scan() && n < 64; n++ {
		line := strings.TrimSpace(sc.Text())
		rest, ok := strings.CutPrefix(line, "# aish:")
		if !ok {
			continue
		}
		key, val, _ := strings.Cut(rest, " ")
		switch key {
		case "desc":
			t.Desc = strings.TrimSpace(val)
			found = true
		case "arg":
			f := strings.SplitN(strings.TrimSpace(val), " ", 3)
			if len(f) < 2 {
				continue
			}
			a := Arg{Name: f[0], Type: f[1], Required: true}
			if len(f) == 3 {
				a.Desc = f[2]
			}
			if strings.HasSuffix(a.Name, "?") {
				a.Name, a.Required = strings.TrimSuffix(a.Name, "?"), false
			}
			if a.Type == "stdin" {
				a.Type, a.Stdin = "string", true
			}
			// As for MCP commands, only required arguments are positional:
			// an optional one left out would shift those after it.
			a.Flag = !a.Required && !a.Stdin
			t.Args = append(t.Args, a)
		}
	}
	return found
}
