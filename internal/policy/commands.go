package policy

import (
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

const maxDepth = 4

// Script is what a policy learns about a bash line as a whole.
type Script struct {
	// Commands holds the argv of every simple command in the line: in
	// pipelines, $(...), subshells, behind wrappers, and in the code the
	// line hands to eval, bash -c, a shell's here-string or here-document,
	// env -S, alias and trap. Words that are not static (expansions,
	// substitutions) are kept in their source form, e.g. "$HOME".
	Commands [][]string
	// Dynamic names what in the line runs code the parser cannot see:
	// "computed", "source", "stdin", "prompt", "depth". Sorted, no
	// repeats; empty when every command is known.
	Dynamic []string
}

// The kinds of Script.Dynamic.
const (
	// dynComputed is a program or code made of expansions: $x -rf,
	// "$(which rm)", eval "$x", bash -c "$x".
	dynComputed = "computed"
	// dynSource is source or ., whose file may change after the check.
	dynSource = "source"
	// dynStdin is a shell reading its commands from stdin: echo … | bash.
	dynStdin = "stdin"
	// dynPrompt is an assignment to a variable the shell runs later.
	dynPrompt = "prompt"
	// dynDepth is code nested deeper than maxDepth, left unparsed.
	dynDepth = "depth"
)

// Commands parses a bash command line and returns the argv of every simple
// command in it, as Parse does.
func Commands(src string) ([][]string, error) {
	s, err := Parse(src)
	return s.Commands, err
}

// Parse parses a bash command line for the policies. The code a line hands
// to eval, bash -c and the like is parsed as a line of its own, down to
// maxDepth, when it is static; what cannot be known before the line runs
// is named in Dynamic. On a parse error, of the line or of the code in it,
// the script holds what was parsed before it.
func Parse(src string) (Script, error) {
	p := &parser{kinds: map[string]bool{}}
	err := p.parse(src, 0)
	s := Script{Commands: p.out}
	for k := range p.kinds {
		s.Dynamic = append(s.Dynamic, k)
	}
	sort.Strings(s.Dynamic)
	return s, err
}

type parser struct {
	out   [][]string
	kinds map[string]bool
}

func (p *parser) mark(kind string) { p.kinds[kind] = true }

func (p *parser) parse(src string, depth int) error {
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(src), "")
	if err != nil {
		return err
	}
	var nested []string
	done := map[*syntax.CallExpr]bool{}
	syntax.Walk(f, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.Stmt:
			// The redirections are the statement's: they tell what a shell
			// it runs reads from stdin.
			if call, ok := n.Cmd.(*syntax.CallExpr); ok {
				done[call] = true
				nested = append(nested, p.call(call, n.Redirs)...)
			}
		case *syntax.CallExpr:
			if !done[n] {
				nested = append(nested, p.call(n, nil)...)
			}
		case *syntax.DeclClause:
			p.decl(n)
		}
		return true
	})
	if len(nested) > 0 && depth >= maxDepth {
		// What is not parsed must not pass for checked.
		p.mark(dynDepth)
		return nil
	}
	for _, s := range nested {
		if err := p.parse(s, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// call records the argv of a simple command and of the commands its
// wrappers run, and returns the code it hands to a shell.
func (p *parser) call(call *syntax.CallExpr, redirs []*syntax.Redirect) []string {
	for _, a := range call.Assigns {
		p.assign(a)
	}
	if len(call.Args) == 0 {
		return nil
	}
	argv := make([]string, len(call.Args))
	static := make([]bool, len(call.Args))
	for i, w := range call.Args {
		argv[i], static[i] = word(w), isStatic(w)
	}
	var code []string
	seen := map[string]bool{}
	for argv != nil {
		p.out = append(p.out, argv)
		found := p.shellC(argv, static)
		found = append(found, p.program(argv, static, redirs)...)
		for _, s := range found {
			if !seen[s] {
				seen[s] = true
				code = append(code, s)
			}
		}
		argv, static = p.next(argv, static)
	}
	return code
}

// program looks at what argv runs and returns the code it hands to eval,
// alias, trap or, on stdin, a shell.
func (p *parser) program(argv []string, static []bool, redirs []*syntax.Redirect) []string {
	if !static[0] {
		p.mark(dynComputed)
		return nil
	}
	args, st := argv[1:], static[1:]
	switch name := filepath.Base(argv[0]); {
	case name == "eval":
		if len(args) > 0 && args[0] == "--" {
			args, st = args[1:], st[1:]
		}
		if slices.Contains(st, false) {
			p.mark(dynComputed)
		} else if len(args) > 0 {
			return []string{strings.Join(args, " ")}
		}
	case name == "source", name == ".":
		// The file is not read: it may change between the check and the run.
		p.mark(dynSource)
	case shells[name]:
		switch _, file, stdin := shellArgs(args); {
		case stdin:
			return p.stdin(redirs)
		case file >= 0 && !st[file]:
			p.mark(dynComputed)
		}
	case name == "env":
		for _, a := range args {
			v, _, ok := strings.Cut(a, "=")
			if !ok && !strings.HasPrefix(a, "-") {
				break
			}
			if ok && promptVars[v] {
				p.mark(dynPrompt)
			}
		}
	case name == "alias":
		var code []string
		for i, a := range args {
			if !st[i] {
				p.mark(dynComputed)
			} else if _, v, ok := strings.Cut(a, "="); ok {
				code = append(code, v)
			}
		}
		return code
	case name == "trap":
		if len(args) > 0 && args[0] == "--" {
			args, st = args[1:], st[1:]
		} else if len(args) > 0 && len(args[0]) > 1 && args[0][0] == '-' {
			return nil // -l, -p: lists, sets nothing
		}
		if slices.Contains(st, false) {
			p.mark(dynComputed)
		} else if len(args) > 1 && args[0] != "-" {
			return []string{args[0]}
		}
	}
	return nil
}

// next returns the command a wrapper in argv runs, with the static flags of
// its words; nil when argv runs none.
func (p *parser) next(argv []string, static []bool) ([]string, []bool) {
	if filepath.Base(argv[0]) == "env" {
		if i, s, ok := envSplit(argv); ok {
			// env -S has quotes, escapes and ${VAR} of its own: only a
			// string of plain words is split here as env splits it.
			if !static[i] || strings.ContainsAny(s, `'"\$`) {
				p.mark(dynComputed)
				return nil, nil
			}
			fields := strings.Fields(s)
			split := append(append([]string{argv[0]}, fields...), argv[i+1:]...)
			st := make([]bool, len(split))
			for k := range st {
				st[k] = true
			}
			copy(st[1+len(fields):], static[i+1:])
			return split, st
		}
	}
	inner := unwrap(argv)
	if inner == nil {
		return nil, nil
	}
	return inner, static[len(argv)-len(inner):]
}

// wrappers run their arguments as a command.
var wrappers = map[string]bool{
	"sudo": true, "doas": true, "env": true, "nohup": true, "time": true, "nice": true,
	"ionice": true, "command": true, "builtin": true, "exec": true, "xargs": true,
	"timeout": true, "stdbuf": true, "setsid": true, "chroot": true, "watch": true,
}

// unwrap returns the command run by a wrapper such as `sudo -u x rm -rf y`,
// skipping its options and VAR=value assignments. Option values are not
// known, so a separate option argument may be taken for the command; that
// errs on the side of more commands for the policy to look at.
func unwrap(argv []string) []string {
	if len(argv) < 2 || !wrappers[filepath.Base(argv[0])] {
		return nil
	}
	name := filepath.Base(argv[0])
	rest := argv[1:]
	for len(rest) > 0 {
		a := rest[0]
		switch {
		case a == "--":
			rest = rest[1:]
			return nonEmptyArgv(rest)
		case strings.HasPrefix(a, "-"):
			rest = rest[1:]
			if name == "sudo" && (a == "-u" || a == "-g" || a == "-C" || a == "-D") && len(rest) > 0 {
				rest = rest[1:]
			}
		case name == "env" && strings.Contains(a, "="):
			rest = rest[1:]
		case name == "timeout" && len(a) > 0 && a[0] >= '0' && a[0] <= '9':
			rest = rest[1:]
		case name == "chroot":
			rest = rest[1:]
			return nonEmptyArgv(rest)
		default:
			return rest
		}
	}
	return nil
}

func nonEmptyArgv(a []string) []string {
	if len(a) == 0 {
		return nil
	}
	return a
}

// shells take their commands from -c, a file or stdin.
var shells = map[string]bool{"bash": true, "sh": true, "zsh": true, "dash": true}

// shellC returns the scripts of `bash -c SCRIPT`, `sh -c`, … wherever a
// shell is in argv: behind sudo or env, and as an argument of find -exec
// or anything else that may run it. A script built at run time is marked.
func (p *parser) shellC(argv []string, static []bool) []string {
	var code []string
	for i, a := range argv {
		if !shells[filepath.Base(a)] {
			continue
		}
		script, _, _ := shellArgs(argv[i+1:])
		switch j := i + 1 + script; {
		case script < 0:
		case static[j]:
			code = append(code, argv[j])
		default:
			p.mark(dynComputed)
		}
	}
	return code
}

// shellArgs reads the arguments of a shell as bash does: options first, -o
// and -O with a value, then the first operand is the script of -c or else
// the file to run. It returns their indexes in args, -1 for none, and
// whether the shell reads its commands from stdin instead.
func shellArgs(args []string) (script, file int, stdin bool) {
	c, s := false, false
	i := 0
options:
	for ; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--" || a == "-":
			i++
			break options
		case a == "--rcfile" || a == "--init-file":
			i++
		case strings.HasPrefix(a, "--"):
		case len(a) > 1 && (a[0] == '-' || a[0] == '+'):
			for _, r := range a[1:] {
				switch r {
				case 'c':
					c = true
				case 's':
					s = true
				case 'o', 'O':
					i++
				}
			}
		default:
			break options
		}
	}
	script, file = -1, -1
	switch {
	case c:
		if i < len(args) {
			script = i
		}
	case s || i >= len(args):
		stdin = true
	default:
		file = i
		stdin = args[i] == "/dev/stdin" || args[i] == "/dev/fd/0" || args[i] == "/proc/self/fd/0"
	}
	return script, file, stdin
}

// stdin returns the code a shell reading its commands from stdin gets from
// the redirections of its statement: a here-string or a here-document. A
// pipe, a file and text built at run time are marked.
func (p *parser) stdin(redirs []*syntax.Redirect) []string {
	var code []string
	fed := false
	for _, r := range redirs {
		if !toStdin(r) {
			continue
		}
		fed = true
		var s string
		var ok bool
		switch r.Op {
		case syntax.WordHdoc:
			s, ok = word(r.Word), isStatic(r.Word)
		case syntax.Hdoc, syntax.DashHdoc:
			s, ok = hdoc(r)
		default:
			p.mark(dynStdin)
			continue
		}
		if ok {
			code = append(code, s)
		} else {
			p.mark(dynComputed)
		}
	}
	if !fed {
		p.mark(dynStdin)
	}
	return code
}

// toStdin tells whether a redirection is of fd 0.
func toStdin(r *syntax.Redirect) bool {
	if r.N != nil {
		return r.N.Value == "0"
	}
	switch r.Op {
	case syntax.RdrIn, syntax.RdrInOut, syntax.DplIn, syntax.Hdoc, syntax.DashHdoc, syntax.WordHdoc:
		return true
	}
	return false
}

// hdoc returns the text of a here-document with no expansions in it as the
// shell reading it gets it. With an unquoted delimiter a backslash before
// $, ` and \ is taken away, so that \$(cmd) runs cmd there.
func hdoc(r *syntax.Redirect) (string, bool) {
	quoted := false
	for _, part := range r.Word.Parts {
		if lit, ok := part.(*syntax.Lit); !ok || strings.Contains(lit.Value, `\`) {
			quoted = true
		}
	}
	if r.Hdoc == nil {
		return "", true
	}
	var b strings.Builder
	for _, part := range r.Hdoc.Parts {
		lit, ok := part.(*syntax.Lit)
		if !ok {
			return "", false
		}
		if quoted {
			b.WriteString(lit.Value)
		} else {
			b.WriteString(unescapeOnly(lit.Value, "$`\\"))
		}
	}
	return b.String(), true
}

// envSplit finds the string of env -S STRING (-SSTRING, --split-string=
// STRING, --split-string STRING) and returns the index of its word in argv.
func envSplit(argv []string) (int, string, bool) {
	for i := 1; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "--":
			return 0, "", false
		case strings.HasPrefix(a, "--"):
			// getopt takes any unambiguous prefix of a long option.
			name, v, eq := strings.Cut(a[2:], "=")
			switch {
			case name == "":
			case strings.HasPrefix("split-string", name):
				if eq {
					return i, v, true
				}
				if i+1 < len(argv) {
					return i + 1, argv[i+1], true
				}
				return 0, "", false
			case !eq && (strings.HasPrefix("unset", name) || strings.HasPrefix("chdir", name) || strings.HasPrefix("argv0", name)):
				i++
			}
		case len(a) > 1 && a[0] == '-':
			for j := 1; j < len(a); j++ {
				switch a[j] {
				case 'S':
					if j+1 < len(a) {
						return i, a[j+1:], true
					}
					if i+1 < len(argv) {
						return i + 1, argv[i+1], true
					}
					return 0, "", false
				case 'u', 'C', 'a':
					// The value is the rest of the word or the next one.
					if j+1 == len(a) {
						i++
					}
					j = len(a)
				}
			}
		case strings.Contains(a, "="):
		default:
			return 0, "", false
		}
	}
	return 0, "", false
}

// promptVars hold code the shell runs later, outside any check: at every
// prompt, or in every bash or sh it starts.
var promptVars = map[string]bool{
	"PROMPT_COMMAND": true, "PS0": true, "PS1": true, "PS2": true, "PS4": true,
	"BASH_ENV": true, "ENV": true,
}

func (p *parser) assign(a *syntax.Assign) {
	if a.Name != nil && !a.Naked && promptVars[a.Name.Value] {
		p.mark(dynPrompt)
	}
}

// decl looks at the assignments of export, declare, local, readonly and
// typeset. Their arguments assign even when quoted whole, as in
// `export 'PS1=$(id)'`, and declare -n r=PS1 makes r another name of PS1.
func (p *parser) decl(d *syntax.DeclClause) {
	nameref := d.Variant.Value == "nameref"
	for _, a := range d.Args {
		switch {
		case a.Name != nil:
			p.assign(a)
			if nameref && a.Value != nil && promptVars[word(a.Value)] {
				p.mark(dynPrompt)
			}
		case a.Value == nil:
		case !isStatic(a.Value):
			p.mark(dynComputed)
		default:
			v := word(a.Value)
			if strings.HasPrefix(v, "-") {
				nameref = nameref || (d.Variant.Value != "export" && strings.Contains(v, "n"))
				continue
			}
			name, _, ok := strings.Cut(v, "=")
			name, _, _ = strings.Cut(name, "[")
			if ok && promptVars[strings.TrimSuffix(name, "+")] {
				p.mark(dynPrompt)
			}
		}
	}
}

// isStatic tells whether a word is the same text whatever the shell's
// state: literals and quotes only, without the globs and braces that make
// another program of /usr/bin/sud? or {sudo,ls}, and without $'\x73udo'.
func isStatic(w *syntax.Word) bool {
	for _, p := range w.Parts {
		switch p := p.(type) {
		case *syntax.Lit:
			if expands(p.Value) {
				return false
			}
		case *syntax.SglQuoted:
			if p.Dollar {
				return false
			}
		case *syntax.DblQuoted:
			if p.Dollar {
				return false
			}
			for _, q := range p.Parts {
				if _, ok := q.(*syntax.Lit); !ok {
					return false
				}
			}
		default:
			return false
		}
	}
	return true
}

// expands tells whether an unquoted literal is subject to pathname or brace
// expansion. A [ without a ] and braces without a comma or .. stay as they
// are: `[ -f x ]`, `find -exec rm {} \;`.
func expands(s string) bool {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '*', '?':
			return true
		case '[':
			if strings.Contains(s[i+1:], "]") {
				return true
			}
		case '{':
			if in, _, ok := strings.Cut(s[i+1:], "}"); ok && (strings.Contains(in, ",") || strings.Contains(in, "..")) {
				return true
			}
		}
	}
	return false
}

func word(w *syntax.Word) string {
	var b strings.Builder
	for _, p := range w.Parts {
		part(&b, p)
	}
	return b.String()
}

func part(b *strings.Builder, p syntax.WordPart) {
	switch p := p.(type) {
	case *syntax.Lit:
		b.WriteString(unescape(p.Value))
	case *syntax.SglQuoted:
		b.WriteString(p.Value)
	case *syntax.DblQuoted:
		for _, q := range p.Parts {
			// In double quotes "\'" keeps its backslash: taken away, it
			// would hide `sudo ls` in bash -c "echo \'; sudo ls; echo \'".
			if lit, ok := q.(*syntax.Lit); ok {
				b.WriteString(unescapeOnly(lit.Value, "$`\"\\"))
				continue
			}
			part(b, q)
		}
	default:
		_ = syntax.NewPrinter().Print(b, p)
	}
}

// unescape removes backslash quoting from an unquoted literal (r\m -> rm).
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// unescapeOnly removes the backslash before the characters of special, as
// bash does in double quotes and here-documents, and a backslash-newline
// altogether; any other backslash stays.
func unescapeOnly(s, special string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			if s[i+1] == '\n' {
				i++
				continue
			}
			if strings.IndexByte(special, s[i+1]) >= 0 {
				i++
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
