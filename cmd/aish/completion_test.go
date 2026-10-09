package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/mcp"
	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/shellinit"
)

// TestCompSpecs: every subcommand run takes has its line in compSpecs, and
// every line is a subcommand, under one that has a line too, with kinds
// compKinds knows. `aish agent` is the shell's own, not the user's.
func TestCompSpecs(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	args0 := func(e ast.Expr) bool {
		ix, ok := e.(*ast.IndexExpr)
		if !ok {
			return false
		}
		id, ok := ix.X.(*ast.Ident)
		lit, ok2 := ix.Index.(*ast.BasicLit)
		return ok && ok2 && id.Name == "args" && lit.Value == "0"
	}
	taken := map[string]bool{}
	add := func(e ast.Expr) {
		if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			s, _ := strconv.Unquote(lit.Value)
			if s != "agent" && !strings.HasPrefix(s, "_") {
				taken[s] = true
			}
		}
	}
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "run" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.SwitchStmt:
				if n.Tag != nil && args0(n.Tag) {
					for _, s := range n.Body.List {
						for _, e := range s.(*ast.CaseClause).List {
							add(e)
						}
					}
				}
			case *ast.BinaryExpr:
				if n.Op == token.EQL && args0(n.X) {
					add(n.Y)
				}
			}
			return true
		})
	}
	if len(taken) < 10 {
		t.Fatalf("found only %v in run", taken)
	}
	for name := range taken {
		if _, ok := specOf(name); !ok {
			t.Errorf("aish %s has no line in compSpecs (cmd/aish/completion.go): add one for Tab to complete it", name)
		}
	}
	for _, s := range compSpecs {
		parent, last, ok := cutLast(s.name)
		switch {
		case !ok:
		case parent == "" && !taken[last]:
			t.Errorf("compSpecs has %q, which run does not take", s.name)
		case parent != "":
			if _, ok := specOf(parent); !ok {
				t.Errorf("compSpecs has %q, but not %q", s.name, parent)
			}
		}
		for _, w := range append(strings.Fields(s.flags), s.args...) {
			for _, k := range regexp.MustCompile(`\{([^}]*)\}`).FindAllStringSubmatch(w, -1) {
				if compKinds[k[1]] == nil {
					t.Errorf("%q: no kind %s in compKinds", s.name, k[0])
				}
			}
		}
	}
}

// completeAt sets up a home with a config.toml whose sessions are in
// sessions: id1 the user named "my work", id2 the model named "auto two",
// id3 with no name. In cwd, the working directory, are a skill and a
// subagent.
func completeAt(t *testing.T) (sessions string) {
	t.Helper()
	sessions = t.TempDir()
	diskConfig(t, "model = \"m-top\"\nsessions_dir = "+strconv.Quote(sessions)+"\n[profiles.work]\nmodel = \"m-work\"\n")
	files := map[string]string{
		filepath.Join(sessions, "id1.jsonl"):                     "",
		filepath.Join(sessions, "id1.name"):                      "my work\n",
		filepath.Join(sessions, "id2.jsonl"):                     "",
		filepath.Join(sessions, "id2.title"):                     "auto two\n",
		filepath.Join(sessions, "id3.jsonl"):                     "",
		filepath.Join(sessions, "bad.id.jsonl"):                  "",
		filepath.Join(".claude", "skills", "deploy", "SKILL.md"): "---\nname: deploy\ndescription: Deploy\n---\nDeploy.\n",
		filepath.Join(".claude", "agents", "reviewer.md"):        "---\nname: reviewer\ndescription: Reviews\n---\nReview.\n",
	}
	for p, s := range files {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return sessions
}

func completions(words ...string) []string {
	c := &completer{ctx: context.Background(), configs: map[string]*config.Config{}}
	c.cwd, _ = os.Getwd()
	if client, err := rpc.FromEnv(); err == nil {
		c.client = client
	}
	return c.complete(words[:len(words)-1], words[len(words)-1])
}

// TestComplete: what Tab offers after aish outside aish, by the files.
func TestComplete(t *testing.T) {
	completeAt(t)
	t.Setenv("AISH_SOCK", "")
	anthropic := []string{"default", "low", "medium", "high", "xhigh", "max"}
	cases := []struct {
		words []string
		want  []string // all of them, in this order
		has   []string // among them
		not   []string // not among them
	}{
		{words: []string{"re"}, has: []string{"resume", "session", "init", "completion"}, not: []string{"", "agent", "rm", "session rm"}},
		{words: []string{"-"}, want: []string{"--resume", "--help"}},
		{words: []string{"bogus", ""}},
		{words: []string{"session", ""}, want: []string{"prune", "rename", "rm", "show"}},
		// Those aish resume lists, the user named; ids when one is typed.
		{words: []string{"session", "rm", ""}, want: []string{"my work"}},
		{words: []string{"session", "rm", "id"}, want: []string{"my work", "id1", "id2", "id3"}},
		{words: []string{"session", "rm", "my work", ""}},
		{words: []string{"session", "rename", ""}, want: []string{"my work", "auto two"}},
		{words: []string{"session", "rename", "id"}, want: []string{"my work", "id1", "auto two", "id2", "id3"}},
		{words: []string{"session", "prune", ""}, want: []string{"--older"}},
		{words: []string{"session", "prune", "--older", ""}},
		{words: []string{"session", "show", ""}},
		{words: []string{"resume", ""}, want: []string{"my work"}},
		{words: []string{"resume", "--all", ""}, want: []string{"my work", "auto two"}},
		{words: []string{"resume", "-a", ""}, want: []string{"my work", "auto two"}},
		{words: []string{"resume", "-"}, want: []string{"--all"}},
		{words: []string{"resume", "id1", ""}, want: []string{"--all"}}, // nothing else fits
		{words: []string{"clear", ""}},
		{words: []string{"recap", ""}},
		{words: []string{"yolo", ""}, want: []string{"off", "on"}},
		{words: []string{"context", ""}, want: []string{"--full"}},
		{words: []string{"init", ""}, want: []string{"bash", "zsh"}},
		{words: []string{"completion", "b"}, want: []string{"bash", "zsh"}},
		{words: []string{"model", ""}, want: append([]string{"root", "work", "m-top"}, anthropic...)},
		{words: []string{"model", "work", ""}, want: append([]string{"m-work"}, anthropic...)},
		{words: []string{"model", "root", ""}, want: append([]string{"m-top"}, anthropic...)},
		{words: []string{"model", "work", "m-work", ""}, want: anthropic},
		{words: []string{"model", "work", "m-work", "high", ""}},
		{words: []string{"policy", "--agent", ""}, want: []string{"reviewer"}},
		{words: []string{"policy", "--agent", "=", ""}, want: []string{"reviewer"}},          // bash: --agent=
		{words: []string{"policy", "--agent=r"}, want: []string{"--agent=reviewer"}},         // zsh
		{words: []string{"policy", "--agent=reviewer", ""}, has: []string{"bash", "deploy"}}, // any tool
		{words: []string{"policy", "--agent", "reviewer", "-"}, want: []string{"--agent"}},
		{words: []string{"tool", ""}, has: []string{"read_file", "deploy"}, not: []string{"bash"}},
		{words: []string{"tool", "read_file", ""}},
		{words: []string{"tasks", ""}, want: []string{"show"}},
		{words: []string{"tasks", "show", ""}}, // no proxy, no tasks
		{words: []string{"trust", ""}, want: []string{"--revoke", "--list"}},
		{words: []string{"agent", "start", "--", ""}},
	}
	for _, c := range cases {
		got := completions(c.words...)
		if c.want != nil || c.has == nil {
			if !slices.Equal(got, c.want) {
				t.Errorf("aish %s: %q, want %q", strings.Join(c.words, " "), got, c.want)
			}
		}
		for _, w := range c.has {
			if !slices.Contains(got, w) {
				t.Errorf("aish %s: %q, without %q", strings.Join(c.words, " "), got, w)
			}
		}
		for _, w := range c.not {
			if slices.Contains(got, w) {
				t.Errorf("aish %s: %q, with %q", strings.Join(c.words, " "), got, w)
			}
		}
	}
}

// TestCompleteInAish: inside aish Tab goes by the proxy: the sessions of
// its directory, but the shell's own for resume, the profiles and the
// model in force, the subagents in the background, the MCP tools it knows.
func TestCompleteInAish(t *testing.T) {
	completeAt(t)
	other := t.TempDir()
	for f, s := range map[string]string{"p1.jsonl": "", "p1.name": "here\n", "p2.jsonl": "", "p2.name": "there\n"} {
		if err := os.WriteFile(filepath.Join(other, f), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	applied := config.Default()
	applied.Model = "m-applied"
	applied.Profiles = map[string]config.Profile{"fast": {}}
	fakeProxy(t, map[string]any{
		rpc.MethodInfo:    rpc.Info{SessionID: "p1", Dir: other, Model: "m-switched"},
		rpc.MethodConfig:  rpc.Config{Config: applied},
		rpc.MethodTasks:   []rpc.Task{{ID: "bg1"}, {ID: "bg2"}},
		rpc.MethodMCPList: mcp.ListResult{Tools: []mcp.ToolInfo{{Name: "srv_lookup"}}},
	})
	cases := []struct {
		words []string
		has   []string
		not   []string
	}{
		{[]string{"resume", ""}, []string{"there"}, []string{"here", "my work"}},
		{[]string{"session", "rename", ""}, []string{"here", "there"}, []string{"my work"}},
		{[]string{"model", ""}, []string{"root", "fast", "m-applied", "m-switched"}, []string{"work", "m-top"}},
		{[]string{"tasks", "show", ""}, []string{"bg1", "bg2"}, nil},
		{[]string{"tool", ""}, []string{"srv_lookup", "deploy"}, nil},
	}
	for _, c := range cases {
		got := completions(c.words...)
		for _, w := range c.has {
			if !slices.Contains(got, w) {
				t.Errorf("aish %s: %q, without %q", strings.Join(c.words, " "), got, w)
			}
		}
		for _, w := range c.not {
			if slices.Contains(got, w) {
				t.Errorf("aish %s: %q, with %q", strings.Join(c.words, " "), got, w)
			}
		}
	}
}

// TestCompleteHelper is aish for the shells of TestCompletionBash, run by
// the script it writes: not a test of its own.
func TestCompleteHelper(t *testing.T) {
	if os.Getenv("AISH_TEST_HELPER") != "1" {
		return
	}
	args := os.Args
	if i := slices.Index(args, "--"); i >= 0 {
		args = args[i+1:]
	}
	os.Exit(run(args))
}

// TestCompletionBash: `aish completion bash` gives a bash --norc, one
// without bash-completion, Tab for the subcommands of aish, quoted; so
// does init.bash under aish. The aish they ask is this one. With
// config.toml broken on disk the script prints and the subcommands come
// all the same.
func TestCompletionBash(t *testing.T) {
	completeAt(t)
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	stub := filepath.Join(dir, "aish")
	script := "#!/bin/sh\nAISH_TEST_HELPER=1 exec " + strconv.Quote(bin) + " -test.run='^TestCompleteHelper$' -- \"$@\"\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	init := filepath.Join(dir, "init.bash")
	if err := os.WriteFile(init, []byte(shellinit.Bash), 0o644); err != nil {
		t.Fatal(err)
	}
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "AISH_") || strings.HasPrefix(kv, "AISH_CONFIG=") {
			env = append(env, kv)
		}
	}
	env = append(env, "PATH="+dir+":"+os.Getenv("PATH"), "PS1=", "HISTFILE=/dev/null")
	// COMP_WORDS as bash splits the line: = is a word of its own.
	tab := func(fn, line string, words ...string) string {
		return "COMP_LINE=" + compQuote(line) + "; COMP_POINT=${#COMP_LINE}; COMP_WORDS=(" + strings.Join(words, " ") + "); COMP_CWORD=$((${#COMP_WORDS[@]} - 1))\n" +
			fn + " aish \"${COMP_WORDS[COMP_CWORD]}\" \"${COMP_WORDS[COMP_CWORD-1]}\"; printf '%s,' \"${COMPREPLY[@]}\"; echo\n"
	}
	// want is all COMPREPLY has, but for one that ends in "...": among
	// them, the subcommands to come aside.
	cases := []struct{ line, want string }{
		{"aish re", "resume,..."},
		{"aish res", "resume,"},
		{"aish session p", "prune,"},
		{"aish resume m", `my\ work,`},
		{"aish resume my\\ w", `my\ work,`},
		{"aish context -", "--full,"},
		{"aish completion ", "bash,zsh,"},
	}
	words := func(line string) []string {
		w := strings.Fields(strings.ReplaceAll(line, "\\ ", "\x00"))
		for i := range w {
			w[i] = compQuote(strings.ReplaceAll(w[i], "\x00", "\\ "))
		}
		if strings.HasSuffix(line, " ") {
			w = append(w, "''")
		}
		return w
	}
	for _, shell := range []struct {
		name, args, setup, fn string
	}{
		{"aish completion bash", "--norc --noprofile", "eval \"$(aish completion bash)\"\n", "_aish"},
		{"init.bash", "--norc --noprofile -i", "AISH_BIN=" + compQuote(stub) + "; source " + compQuote(init) + "\n", "__aish_comp_aish"},
	} {
		in := shell.setup + "complete -p aish\n"
		for _, c := range cases {
			in += tab(shell.fn, c.line, words(c.line)...)
		}
		cmd := exec.Command("bash", strings.Fields(shell.args)...)
		cmd.Dir = dir
		cmd.Env = env
		cmd.Stdin = strings.NewReader(in)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s: %v\n%s", shell.name, err, out)
		}
		out = regexp.MustCompile("\x1b]6973;[^\a]*\a").ReplaceAll(out, nil)
		lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
		if want := "complete -F " + shell.fn + " aish"; len(lines) == 0 || lines[0] != want {
			t.Errorf("%s: %q, want %q first", shell.name, lines, want)
			continue
		}
		for i, c := range cases {
			got := compAt(lines, i+1)
			if want, more := strings.CutSuffix(c.want, "..."); got != c.want && !(more && strings.Contains(","+got, ","+want)) {
				t.Errorf("%s: %q: %q, want %q", shell.name, c.line, got, c.want)
			}
		}
	}

	if err := os.WriteFile(os.Getenv("AISH_CONFIG"), []byte("max_steps = [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"completion", "bash"}, "complete -F _aish aish"},
		{[]string{"__complete", "aish", "re"}, "\nresume\n"},
	} {
		cmd := exec.Command(stub, c.args...)
		cmd.Env = env
		out, err := cmd.Output()
		if err != nil || !strings.Contains(string(out), c.want) {
			t.Errorf("aish %s with config.toml broken: %v\n%s", strings.Join(c.args, " "), err, out)
		}
	}
}

func compQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func compAt(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return "<none>"
}
