package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// external loads a tool that prints its argv one word per line, then
// AISH_ARG_B and its standard input.
func external(t *testing.T) Tool {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"# aish:desc Echo the arguments\n" +
		"# aish:arg a string First\n" +
		"# aish:arg b? integer Optional, between required ones\n" +
		"# aish:arg c string Second required\n" +
		"# aish:arg v? boolean Verbose\n" +
		"# aish:arg body? stdin Text\n" +
		"for x; do printf '%s\\n' \"$x\"; done\n" +
		"printf 'B=%s\\n' \"${AISH_ARG_B-unset}\"\n" +
		"cat\n"
	if err := os.WriteFile(filepath.Join(dir, "echo"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ts := loadExternal(dir)
	if len(ts) != 1 {
		t.Fatalf("loaded %d tools", len(ts))
	}
	return ts[0]
}

func TestExternalArgs(t *testing.T) {
	tool := external(t)
	if u := tool.Usage(); u != "echo A [--b B] C [--v] [BODY|-]" {
		t.Errorf("usage %q", u)
	}
	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		// From the model: JSON numbers, and null for an argument not given.
		{map[string]any{"a": "x", "c": "y z"}, "x\ny z\nB=unset\n"},
		{map[string]any{"a": "x", "b": nil, "c": "y z", "v": false}, "x\ny z\nB=unset\n"},
		{map[string]any{"a": "x", "b": 3.0, "c": "y z", "v": true, "body": "text\n"},
			"x\ny z\n--b\n3\n--v\nB=3\ntext\n"},
		{map[string]any{"a": "--b", "b": -1.0, "c": "--", "v": "true"}, "--b\n--\n--b\n-1\n--v\nB=-1\n"},
	} {
		out, err := tool.Execute(context.Background(), tc.args, nil)
		if err != nil {
			t.Errorf("%v: %v", tc.args, err)
		}
		if out != tc.want {
			t.Errorf("%v: got %q, want %q", tc.args, out, tc.want)
		}
	}
	if _, err := tool.Execute(context.Background(), map[string]any{"a": "x", "b": 1.0}, nil); err == nil ||
		!strings.Contains(err.Error(), "missing argument c") {
		t.Errorf("missing c: %v", err)
	}
}

// The tool gets the command line `aish tool` reads, so parsing what it got
// gives back the arguments typed.
func TestExternalRoundTrip(t *testing.T) {
	tool := external(t)
	for _, argv := range [][]string{
		{"x", "y"},
		{"--b", "7", "x", "y"},
		{"x", "--v", "y", "--b=7", "body"},
	} {
		args, err := tool.ParseCLI(argv, strings.NewReader("stdin"))
		if err != nil {
			t.Fatalf("%q: %v", argv, err)
		}
		out, err := tool.Execute(context.Background(), args, nil)
		if err != nil {
			t.Fatalf("%q: %v", argv, err)
		}
		got := strings.Split(out, "\n")
		got = got[:len(got)-2] // B=…, then stdin with no newline
		back, err := tool.ParseCLI(got, strings.NewReader(args["body"].(string)))
		if err != nil {
			t.Fatalf("%q: parsing %q: %v", argv, got, err)
		}
		if !reflect.DeepEqual(back, args) {
			t.Errorf("%q: the tool got %q, which parses as %v, not %v", argv, got, back, args)
		}
	}
}

// search has every kind of argument the CLI form knows.
var search = Tool{Name: "search", Args: []Arg{
	{Name: "query", Type: "string", Required: true},
	{Name: "limit", Type: "integer", Flag: true},
	{Name: "all", Type: "boolean", Flag: true},
	{Name: "filter", Type: "object", Flag: true},
	{Name: "body", Type: "string", Stdin: true},
}}

func TestParseCLI(t *testing.T) {
	positional := Tool{Name: "read", Args: []Arg{
		{Name: "path", Type: "string", Required: true},
		{Name: "offset", Type: "integer"},
		{Name: "ratio", Type: "number"},
		{Name: "force", Type: "boolean"},
	}}
	typed := Tool{Name: "typed", Args: []Arg{
		{Name: "list", Type: "array"},
		{Name: "v", Type: "any"},
	}}
	rest := Tool{Name: "skill", Args: []Arg{
		{Name: "first", Type: "string", Required: true},
		{Name: "arguments", Type: "string", Rest: true},
	}}
	required := Tool{Name: "need", Args: []Arg{{Name: "token", Type: "string", Flag: true, Required: true}}}

	for _, tc := range []struct {
		name  string
		tool  Tool
		argv  []string
		stdin string
		want  map[string]any
		err   string
	}{
		{"positional", positional, []string{"f", "3", "0.5", "true"}, "", map[string]any{"path": "f", "offset": 3, "ratio": 0.5, "force": true}, ""},
		{"optional left out", positional, []string{"f"}, "", map[string]any{"path": "f"}, ""},
		{"no required", positional, nil, "", nil, "usage: read PATH [OFFSET] [RATIO] [FORCE]"},
		{"extra positional", positional, []string{"f", "1", "1", "true", "more"}, "", nil, "usage: read"},
		{"bad positional", positional, []string{"f", "x"}, "", nil, `OFFSET: strconv.Atoi: parsing "x": invalid syntax`},
		{"bad boolean", positional, []string{"f", "1", "1", "yes"}, "", nil, `FORCE: strconv.ParseBool: parsing "yes"`},
		// Without flags -- is an argument like any other.
		{"dashes without flags", positional, []string{"--"}, "", map[string]any{"path": "--"}, ""},

		{"flag value", search, []string{"--limit", "5", "q", "b"}, "", map[string]any{"query": "q", "limit": 5, "body": "b"}, ""},
		{"flag=value", search, []string{"q", "--limit=5", "b"}, "", map[string]any{"query": "q", "limit": 5, "body": "b"}, ""},
		{"boolean flag", search, []string{"--all", "q", "b"}, "", map[string]any{"query": "q", "all": true, "body": "b"}, ""},
		{"boolean flag=false", search, []string{"--all=false", "q", "b"}, "", map[string]any{"query": "q", "all": false, "body": "b"}, ""},
		{"json flag", search, []string{"--filter", `{"a":1}`, "q", "b"}, "", map[string]any{"query": "q", "filter": map[string]any{"a": 1.0}, "body": "b"}, ""},
		{"bad json flag", search, []string{"--filter", "{", "q"}, "", nil, "--filter: invalid JSON"},
		{"bad flag", search, []string{"--limit", "x", "q"}, "", nil, `--limit: strconv.Atoi: parsing "x": invalid syntax`},
		{"flag without value", search, []string{"q", "--limit"}, "", nil, "usage: search"},
		{"dashes end flags", search, []string{"--all", "--", "--limit", "b"}, "", map[string]any{"query": "--limit", "all": true, "body": "b"}, ""},
		{"unknown flag is positional", search, []string{"--nope", "b"}, "", map[string]any{"query": "--nope", "body": "b"}, ""},
		{"single dash is positional", search, []string{"-limit", "b"}, "", map[string]any{"query": "-limit", "body": "b"}, ""},
		{"stdin given", search, []string{"q", "arg"}, "from stdin", map[string]any{"query": "q", "body": "arg"}, ""},
		{"stdin read", search, []string{"q"}, "from\nstdin\n", map[string]any{"query": "q", "body": "from\nstdin\n"}, ""},
		{"required flag", required, []string{"--token", "t"}, "", map[string]any{"token": "t"}, ""},
		{"required flag missing", required, nil, "", nil, "usage: need --token TOKEN"},

		{"array", typed, []string{`[1,"a"]`}, "", map[string]any{"list": []any{1.0, "a"}}, ""},
		{"any as json", typed, []string{"[]", `{"k":true}`}, "", map[string]any{"list": []any{}, "v": map[string]any{"k": true}}, ""},
		{"any as string", typed, []string{"[]", "plain text"}, "", map[string]any{"list": []any{}, "v": "plain text"}, ""},

		{"rest", rest, []string{"a", "b", "c d", "it's", ""}, "", map[string]any{"first": "a", "arguments": `b 'c d' 'it'\''s' ''`}, ""},
		{"rest empty", rest, []string{"a"}, "", map[string]any{"first": "a"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.tool.ParseCLI(tc.argv, strings.NewReader(tc.stdin))
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("error %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %#v\nwant %#v", got, tc.want)
			}
		})
	}
}

func TestParseCLIStdinError(t *testing.T) {
	_, err := search.ParseCLI([]string{"q"}, errReader{})
	if err == nil || err.Error() != "broken" {
		t.Errorf("error %v, want the reader's", err)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("broken") }

func TestUsage(t *testing.T) {
	tool := search
	tool.Args = append(tool.Args, Arg{Name: "rest", Rest: true})
	want := "search QUERY [--limit LIMIT] [--all] [--filter JSON] [BODY|-] [REST...]"
	if got := tool.Usage(); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestSchema(t *testing.T) {
	tool := Tool{Args: []Arg{
		{Name: "path", Type: "string", Desc: "File path", Required: true},
		{Name: "n", Type: "integer", Desc: "Count"},
		{Name: "untyped"},
	}}
	want := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":    map[string]any{"type": "string", "description": "File path"},
			"n":       map[string]any{"type": "integer", "description": "Count"},
			"untyped": map[string]any{"type": "string", "description": ""},
		},
		"required": []string{"path"},
	}
	if got := tool.Schema(); !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v\nwant %#v", got, want)
	}

	// The API takes "required": [] but not null.
	b, _ := json.Marshal(Tool{}.Schema())
	if !strings.Contains(string(b), `"required":[]`) {
		t.Errorf("no arguments: %s", b)
	}

	tool.RawSchema = json.RawMessage(`{"type":"object","properties":{"q":{"type":"string","enum":["a","b"]}}}`)
	want = map[string]any{"type": "object", "properties": map[string]any{
		"q": map[string]any{"type": "string", "enum": []any{"a", "b"}},
	}}
	if got := tool.Schema(); !reflect.DeepEqual(got, want) {
		t.Errorf("raw schema: got %#v", got)
	}

	tool.RawSchema = json.RawMessage(`not json`)
	if got := tool.Schema(); got["required"] == nil {
		t.Errorf("a broken raw schema should fall back to Args: %#v", got)
	}
}

func TestDecode(t *testing.T) {
	for _, raw := range []string{"", "{}"} {
		args, err := Decode(json.RawMessage(raw))
		if err != nil || args == nil || len(args) != 0 {
			t.Errorf("%q: %v, %v", raw, args, err)
		}
	}
	args, err := Decode(json.RawMessage(`{"path":"f","limit":3}`))
	if err != nil || args["path"] != "f" || num(args, "limit") != 3 {
		t.Errorf("got %v, %v", args, err)
	}
	if _, err := Decode(json.RawMessage(`{"path":`)); err == nil || !strings.Contains(err.Error(), "invalid tool arguments") {
		t.Errorf("broken JSON: %v", err)
	}
}

func TestArgHelpers(t *testing.T) {
	args := map[string]any{"f": 3.0, "i": 4, "s": "5", "bad": "x", "b": true, "bs": "true", "n": nil}
	for k, want := range map[string]int{"f": 3, "i": 4, "s": 5, "bad": 0, "b": 0, "none": 0} {
		if got := num(args, k); got != want {
			t.Errorf("num %s = %d, want %d", k, got, want)
		}
	}
	for k, want := range map[string]bool{"b": true, "bs": true, "s": false, "f": false, "none": false} {
		if got := boolean(args, k); got != want {
			t.Errorf("boolean %s = %v, want %v", k, got, want)
		}
	}
	if str(args, "s") != "5" || str(args, "f") != "" || str(args, "n") != "" {
		t.Errorf("str: %q %q", str(args, "s"), str(args, "f"))
	}
}

// writeExec writes a file with exactly mode, whatever the umask.
func writeExec(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestParseHeader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "issues")
	writeExec(t, path, "#!/bin/sh\n"+
		"# aish:desc   Search the issue tracker  \n"+
		"# aish:arg query string Text to search for\n"+
		"  # aish:arg limit? integer Maximum number of results\n"+
		"# aish:arg body stdin Text passed on standard input\n"+
		"# aish:arg bare string\n"+
		"# aish:arg broken\n"+
		"# aish:other ignored\n"+
		"#aish:arg nospace string\n"+
		"exit 0\n", 0o755)
	tool := Tool{Name: "issues", Path: path}
	if !parseHeader(path, &tool) {
		t.Fatal("header not found")
	}
	want := Tool{Name: "issues", Path: path, Desc: "Search the issue tracker", Args: []Arg{
		{Name: "query", Type: "string", Desc: "Text to search for", Required: true},
		{Name: "limit", Type: "integer", Desc: "Maximum number of results", Flag: true},
		{Name: "body", Type: "string", Desc: "Text passed on standard input", Required: true, Stdin: true},
		{Name: "bare", Type: "string", Required: true},
	}}
	if !reflect.DeepEqual(tool, want) {
		t.Errorf("got  %+v\nwant %+v", tool, want)
	}

	writeExec(t, path, "#!/bin/sh\n# aish:arg query string Text\n", 0o755)
	if parseHeader(path, &Tool{}) {
		t.Error("a file without # aish:desc is not a tool")
	}

	// Only the head of the file is read: a binary or a long script is not
	// scanned to the end.
	head := "#!/bin/sh\n" + strings.Repeat("echo\n", 62)
	writeExec(t, path, head+"# aish:desc Within the head\n# aish:arg late string Past the head\n", 0o755)
	tool = Tool{}
	if !parseHeader(path, &tool) || tool.Desc != "Within the head" || len(tool.Args) != 0 {
		t.Errorf("line 64 is read, line 65 is not: %+v", tool)
	}
	writeExec(t, path, head+"echo\n# aish:desc Too late\n", 0o755)
	if parseHeader(path, &Tool{}) {
		t.Error("# aish:desc on line 65 should not count")
	}

	if parseHeader(filepath.Join(dir, "missing"), &Tool{}) {
		t.Error("a missing file has no header")
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	tool := "#!/bin/sh\n# aish:desc A tool\n"
	writeExec(t, filepath.Join(dir, "zeta"), tool, 0o755)
	writeExec(t, filepath.Join(dir, "alpha"), tool, 0o700)
	writeExec(t, filepath.Join(dir, "plain"), tool, 0o644)
	writeExec(t, filepath.Join(dir, "noheader"), "#!/bin/sh\necho\n", 0o755)
	writeExec(t, filepath.Join(dir, "read_file"), tool, 0o755)
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "zeta"), filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}

	r := Load(dir)
	var names []string
	for _, tool := range r.All() {
		names = append(names, tool.Name)
	}
	builtins := []string{Bash, "read_file", "write_file", "edit_file"}
	want := append(builtins, "alpha", "link", "zeta")
	if !reflect.DeepEqual(names, want) {
		t.Errorf("got %v, want %v", names, want)
	}
	if rf, _ := r.Get("read_file"); rf.Run == nil || rf.Path != "" {
		t.Errorf("an external tool replaced a built-in: %+v", rf)
	}
	if z, ok := r.Get("zeta"); !ok || z.Path != filepath.Join(dir, "zeta") || z.Desc != "A tool" {
		t.Errorf("zeta: %+v", z)
	}
	if r.Add(Tool{Name: "alpha"}) {
		t.Error("Add took a duplicate")
	}
	if _, ok := r.Get("nope"); ok {
		t.Error("Get found a tool that is not there")
	}

	if got := len(Load(filepath.Join(dir, "missing")).All()); got != len(builtins) {
		t.Errorf("no tools directory: %d tools", got)
	}
}

func TestRunExternal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "probe")
	writeExec(t, path, `#!/bin/sh
echo "argc=$#"
for a; do echo "arg=$a"; done
echo "env=$AISH_ARG_CITY|$AISH_ARG_DAYS|$AISH_ARG_BODY"
printf 'stdin=%s\n' "$(cat)"
echo "to stderr" >&2
`, 0o755)
	tool := Tool{Name: "probe", Path: path, Args: []Arg{
		{Name: "city", Type: "string", Required: true},
		{Name: "days", Type: "integer", Flag: true},
		{Name: "body", Type: "string", Stdin: true},
	}}

	var live strings.Builder
	out, err := tool.Execute(context.Background(), map[string]any{"city": "New York", "days": 3, "body": "line 1\nline 2"}, &live)
	if err != nil {
		t.Fatal(err)
	}
	want := "argc=3\narg=New York\narg=--days\narg=3\nenv=New York|3|line 1\nline 2\nstdin=line 1\nline 2\nto stderr\n"
	if out != want {
		t.Errorf("got\n%s\nwant\n%s", out, want)
	}
	if live.String() != out {
		t.Errorf("live output %q differs from the result", live.String())
	}

	// From the model, numbers arrive as float64.
	out, err = tool.Execute(context.Background(), map[string]any{"city": "Oslo", "days": 2.0}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := "argc=3\narg=Oslo\narg=--days\narg=2\nenv=Oslo|2|\nstdin=\nto stderr\n"; out != want {
		t.Errorf("without stdin: got\n%s\nwant\n%s", out, want)
	}
}

func TestRunExternalFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fail")
	writeExec(t, path, "#!/bin/sh\necho partial\nexit 3\n", 0o755)
	tool := Tool{Name: "fail", Path: path}
	out, err := tool.Execute(context.Background(), nil, nil)
	if out != "partial\n" {
		t.Errorf("output %q should be kept", out)
	}
	if err == nil || err.Error() != "fail: exit status 3" {
		t.Errorf("error %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tool.Execute(ctx, nil, nil); err == nil {
		t.Error("a cancelled context should not run the tool")
	}
}

// The agent runs in the proxy, not in the shell: tools take the shell's
// directory and environment from Exec.
func TestExecuteIn(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	read, _ := Load("").Get("read_file")
	ex := Exec{Dir: dir, Env: []string{"AISH_PROBE=yes", "PATH=" + os.Getenv("PATH")}}
	args := map[string]any{"path": "f.txt"}
	out, err := read.ExecuteIn(context.Background(), ex, args, nil)
	if err != nil || !strings.Contains(out, "hello") {
		t.Errorf("relative path from Exec.Dir: %q, %v", out, err)
	}
	if args["path"] != "f.txt" {
		t.Errorf("the caller's args were changed: %v", args)
	}
	if _, err := read.Execute(context.Background(), args, nil); err == nil {
		t.Error("without Exec the path is relative to the process")
	}

	path := filepath.Join(dir, "probe")
	writeExec(t, path, "#!/bin/sh\npwd\necho \"$AISH_PROBE\"\n", 0o755)
	tool := Tool{Name: "probe", Path: path}
	out, err = tool.ExecuteIn(context.Background(), ex, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(dir)
	if got := strings.Split(strings.TrimSpace(out), "\n"); len(got) != 2 || got[0] != real && got[0] != dir || got[1] != "yes" {
		t.Errorf("external tool ran as %q, want in %s with the given environment", out, dir)
	}
	if got := ex.Getenv("AISH_PROBE"); got != "yes" {
		t.Errorf("Getenv %q", got)
	}
	if got := (Exec{}).Getenv("PATH"); got != os.Getenv("PATH") {
		t.Errorf("Getenv without Env is the process's: %q", got)
	}
}

func TestExecuteBash(t *testing.T) {
	bash, _ := Load("").Get(Bash)
	if _, err := bash.Execute(context.Background(), map[string]any{"command": "true"}, nil); err == nil {
		t.Error("bash runs in the user's shell, not in the agent")
	}
}
