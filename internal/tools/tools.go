// Package tools defines what the agent can do. The user can call a tool,
// too, as `aish tool NAME`; user tools are plain executables in the tools
// directory. A tool is a Tool; what sets a kind of tools apart for the
// agent (a command handed to the shell, output shown as it comes, a schema
// kept from the model) it tells by an optional interface, so that a new
// kind needs no case in the agent, the proxy or the commands.
package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Bash is the tool whose commands run in the user's live shell, driven by
// the agent.
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

// Tool is something the agent can call. The user can call it, too, as
// `aish tool NAME`, unless it hands its calls off to the shell or asks the
// user.
type Tool interface {
	Name() string
	Desc() string
	// Args are the input as a command takes it: see ParseCLI.
	Args() []Arg
	// Schema is the JSON schema of the input, as the model is given it.
	Schema() map[string]any
	// Execute runs the tool in ex and returns its textual result. Live,
	// when not nil, takes the output as it comes, if the tool is Streaming.
	Execute(ctx context.Context, ex Exec, args map[string]any, live io.Writer) (string, error)
}

// HandsOff tools are not executed by the agent: a call is a command for the
// user's live shell, which runs it as if typed (bash).
type HandsOff interface {
	// Command is the command line for args; false when there is none.
	Command(args map[string]any) (string, bool)
}

// Dialog tools are answered by the user instead of run: the agent shows a
// call as a form on its terminal, and what the user chose is the result
// (ask_user). Only the agent has that terminal, so the user cannot call
// them.
type Dialog interface{ Dialog() bool }

// Hidden tools are deferred: their schemas are not sent with every request.
// The model sees their names in the system prompt and loads the ones it
// needs with tool_search; from then on they are offered as any other tool.
type Hidden interface{ Hidden() bool }

// Origin is where a tool comes from, for the policy: its MCP server.
type Origin interface{ Server() string }

// Instructed tools come with what their origin says about their use: an
// MCP server's instructions for all its tools, which go into the system
// prompt once for the server.
type Instructed interface{ Instructions() string }

// Streaming tools write their output to Execute's live writer as they
// run, like a command; the agent shows it folded instead of a summary.
type Streaming interface{ Streaming() bool }

// Titled tools show their calls their own way: see Title.
type Titled interface {
	Title(args map[string]any) string
}

func IsDialog(t Tool) bool {
	d, ok := t.(Dialog)
	return ok && d.Dialog()
}

func IsHidden(t Tool) bool {
	h, ok := t.(Hidden)
	return ok && h.Hidden()
}

func Streams(t Tool) bool {
	s, ok := t.(Streaming)
	return ok && s.Streaming()
}

// ServerOf is the MCP server of t, "" for a tool of aish or the user.
func ServerOf(t Tool) string {
	if o, ok := t.(Origin); ok {
		return o.Server()
	}
	return ""
}

// InstructionsOf is what t says about its use, "" for nothing.
func InstructionsOf(t Tool) string {
	if i, ok := t.(Instructed); ok {
		return i.Instructions()
	}
	return ""
}

// Title is a call as shown to the user: the tool's own title, or its name
// and the arguments given, a long one as its size.
func Title(t Tool, args map[string]any) string {
	if tt, ok := t.(Titled); ok {
		return tt.Title(args)
	}
	parts := []string{t.Name()}
	for _, a := range t.Args() {
		v, ok := args[a.Name]
		if !ok {
			continue
		}
		s := fmt.Sprint(v)
		if a.Stdin || len(s) > 80 || strings.Contains(s, "\n") {
			s = fmt.Sprintf("<%d bytes>", len(s))
		}
		parts = append(parts, s)
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// Schema is the JSON schema of an input of form.
func Schema(form []Arg) map[string]any {
	props := map[string]any{}
	required := []string{}
	for _, a := range form {
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
	if _, dup := r.byID[t.Name()]; dup {
		return false
	}
	if r.byID == nil {
		r.byID = map[string]Tool{}
	}
	r.list = append(r.list, t)
	r.byID[t.Name()] = t
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
	// Opts are the options of set -o and shopt the shell has on, by name,
	// as its last prompt had them (zsh's by zsh's names); nil when not
	// known. The policy reads the commands handed to the shell as it runs
	// them in these.
	Opts []string
	// Shell names the shell the commands handed off run in, "bash" or
	// "zsh": the policy reads them as it does. "" is bash.
	Shell string
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

// ParseCLI maps command-line arguments to tool arguments: positional ones in
// declaration order, a Rest one taking what is left, flags as --name VALUE or
// --name=VALUE; a Stdin argument that is not given is read from stdin. Name
// and form are the tool's, as for Usage.
func ParseCLI(name string, form []Arg, argv []string, stdin io.Reader) (map[string]any, error) {
	args := map[string]any{}
	usage := fmt.Errorf("usage: %s", Usage(name, form))
	flags := map[string]Arg{}
	for _, a := range form {
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
	for _, a := range form {
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

// Usage is the command line of a tool called name whose input is form.
func Usage(name string, form []Arg) string {
	var b strings.Builder
	b.WriteString(name)
	for _, a := range form {
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
func loadExternal(dir string) []external {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []external
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		// A FIFO is left out: reading its header would wait for a writer,
		// and every request with it.
		st, err := os.Stat(path)
		if err != nil || !st.Mode().IsRegular() || st.Mode()&0o111 == 0 {
			continue
		}
		t := external{name: e.Name(), path: path}
		if !parseHeader(path, &t) {
			continue
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

func parseHeader(path string, t *external) bool {
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
			t.desc = strings.TrimSpace(val)
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
			t.args = append(t.args, a)
		}
	}
	return found
}
