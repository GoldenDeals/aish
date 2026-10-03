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
	// env -S, alias, trap, su -c, flock -c, script -c, watch and, on
	// another machine, ssh HOST CMD. Words that are not static
	// (expansions, substitutions) are kept in their source form, e.g.
	// "$HOME".
	Commands [][]string
	// Remote holds the indexes in Commands of the commands that run on
	// another machine, in the command of ssh: their words name no files
	// of this one, and their redirections are not in Writes.
	Remote []int
	// Dynamic names what in the line runs code the parser cannot see:
	// "computed", "source", "stdin", "prompt", "depth". Sorted, no
	// repeats; empty when every command is known.
	Dynamic []string
	// Writes holds the files the line writes by redirections (>, >>, &>,
	// <>, …) wherever they are, absolute and resolved as the kernel opens
	// them; a file not known before the line runs is marked "computed"
	// instead. Empty without a cwd.
	Writes []string
	// UnknownWrite tells that a redirection writes such a file: one built
	// of expansions (> "$f") or a relative one in a line with a cd. Dynamic
	// has "computed" for a program built at run time too, which writes no
	// file a policy could know of.
	UnknownWrite bool
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
	s, err := Parse(src, "", "")
	return s.Commands, err
}

// Parse parses a bash command line for the policies, as run from cwd by a
// user whose home is home; both absolute and resolved, home "" is no home.
// The code a line hands to eval, bash -c and the like is parsed as a line
// of its own, down to maxDepth, when it is static; what cannot be known
// before the line runs is named in Dynamic. On a parse error, of the line
// or of the code in it, the script holds what was parsed before it.
func Parse(src, cwd, home string) (Script, error) {
	p := &parser{kinds: map[string]bool{}, cwd: cwd, home: home}
	err := p.parse(src, 0, false)
	s := Script{Commands: p.out, Remote: p.remotes}
	for _, t := range p.writes {
		switch {
		case t.rel && p.chdir:
			// A cd anywhere in the line may run before the write: the
			// directory it writes in is not cwd any longer.
			p.mark(dynComputed)
			p.unknown = true
		case cwd != "":
			if path := walk(t.path); !slices.Contains(s.Writes, path) {
				s.Writes = append(s.Writes, path)
			}
		}
	}
	s.UnknownWrite = p.unknown
	for k := range p.kinds {
		s.Dynamic = append(s.Dynamic, k)
	}
	sort.Strings(s.Dynamic)
	return s, err
}

type parser struct {
	out       [][]string
	kinds     map[string]bool
	cwd, home string
	// writes are the files of the redirections that write, as spelled.
	writes []write
	// unknown is a redirection to a file known only at run time.
	unknown bool
	// chdir is a cd, pushd or popd somewhere in the line.
	chdir bool
	// remote tells that the code being walked runs on another machine,
	// remotes are the indexes in out of the commands that run there.
	remote  bool
	remotes []int
}

// snippet is code a line hands to a shell, here or on another machine.
type snippet struct {
	src    string
	remote bool
}

// write is the file a redirection writes: path is absolute but not
// resolved, rel tells that it is taken from the current directory.
type write struct {
	path string
	rel  bool
}

func (p *parser) mark(kind string) { p.kinds[kind] = true }

func (p *parser) parse(src string, depth int, remote bool) error {
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(src), "")
	if err != nil {
		return err
	}
	p.remote = remote
	var nested []snippet
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
		case *syntax.Redirect:
			// Of any statement: { …; } > f and done > f write too.
			p.redirect(n)
		}
		return true
	})
	if len(nested) > 0 && depth >= maxDepth {
		// What is not parsed must not pass for checked.
		p.mark(dynDepth)
		return nil
	}
	for _, s := range nested {
		if err := p.parse(s.src, depth+1, s.remote); err != nil {
			return err
		}
	}
	return nil
}

// call records the argv of a simple command and of the commands its
// wrappers run, and returns the code it hands to a shell.
func (p *parser) call(call *syntax.CallExpr, redirs []*syntax.Redirect) []snippet {
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
	var code []snippet
	seen := map[snippet]bool{}
	add := func(src string, remote bool) {
		if s := (snippet{src, remote}); !seen[s] {
			seen[s] = true
			code = append(code, s)
		}
	}
	for argv != nil {
		if p.remote {
			p.remotes = append(p.remotes, len(p.out))
		}
		p.out = append(p.out, argv)
		if !p.remote && static[0] && chdirs[filepath.Base(argv[0])] {
			p.chdir = true
		}
		here, there, local := p.handed(argv, static)
		found := p.shellC(argv[:local], static[:local])
		found = append(found, p.program(argv, static, redirs)...)
		for _, s := range append(found, here...) {
			add(s, p.remote)
		}
		for _, s := range there {
			add(s, true)
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

// strung are the commands that run a string of theirs through a shell:
// what finds the string among their arguments, and whether that shell is
// on another machine.
var strung = map[string]struct {
	find   func(args []string) []piece
	remote bool
}{
	"ssh":    {sshCommand, true},
	"su":     {suCommand, false},
	"flock":  {flockCommand, false},
	"script": {scriptCommand, false},
	"watch":  {watchCommand, false},
}

// piece is a string a command runs through a shell, and the indexes of
// the words of its arguments the string is made of.
type piece struct {
	text  string
	words []int
}

// handed returns the strings the commands of strung in argv run, wherever
// they are in it, as shellC finds shells: here those of su -c, flock -c,
// script -c and watch, there the command of ssh. A string with a word
// built at run time is marked. As the program of argv such a command is
// marked for a word of that kind anywhere, which word splitting may make
// an option, the host or a string. local is how many words of argv run
// here: after an ssh that is the program, none of them is a shell of
// this machine.
func (p *parser) handed(argv []string, static []bool) (here, there []string, local int) {
	for i, a := range argv {
		s, ok := strung[filepath.Base(a)]
		if !ok {
			continue
		}
		st := static[i+1:]
		known := !slices.Contains(st, false)
		if i == 0 && !known {
			p.mark(dynComputed)
		}
		for _, pc := range s.find(argv[i+1:]) {
			switch {
			case slices.ContainsFunc(pc.words, func(w int) bool { return !st[w] }):
				p.mark(dynComputed)
			case s.remote:
				there = append(there, pc.text)
			default:
				here = append(here, pc.text)
			}
		}
		if i == 0 && s.remote && known {
			return here, there, 1
		}
	}
	return here, there, len(argv)
}

// The options of the commands of strung, as their getopt calls read them
// in OpenSSH 10, util-linux 2.42 and procps-ng 4.
var (
	sshOpts    = getopt{short: "+1246ab:c:e:fgi:kl:m:no:p:qstvxAB:CD:E:F:GI:J:KL:MNO:P:Q:R:S:TVw:W:XYy"}
	suOpts     = getopt{short: "c:fg:G:lmpPTs:u:hVw:", long: "command: session-command: fast login preserve-environment pty no-pty shell: group: supp-group: user: whitelist-environment: help version"}
	flockOpts  = getopt{short: "+sexnoFuw:E:hV?", long: "shared exclusive unlock nonblocking nb timeout: wait: conflict-exit-code: close no-fork verbose fcntl start: length: help version"}
	scriptOpts = getopt{short: "aB:c:eE:fI:O:o:qm:T:t::Vh", long: "append command: echo: return flush force quiet log-in: log-out: log-io: log-timing: logging-format: output-limit: timing:: help version"}
	watchOpts  = getopt{short: "+bCcefd::ghq:n:prs:twvx", long: "beep color no-color differences:: errexit follow chgexit interval: precise equexit: no-rerun shotsdir: no-title no-wrap exec help version"}
)

// sshCommand finds the command of ssh [OPTIONS] HOST [OPTIONS] CMD...: ssh
// reads options again after the host, unless a "--" ended them, and joins
// the rest with spaces for the shell over there.
func sshCommand(args []string) []piece {
	_, ops := sshOpts.read(args)
	if len(ops) < 2 {
		return nil
	}
	from := ops[1]
	if host := ops[0]; host == 0 || args[host-1] != "--" {
		_, more := sshOpts.read(args[from:])
		if len(more) == 0 {
			return nil
		}
		from += more[0]
	}
	return []piece{{strings.Join(args[from:], " "), indexes(from, len(args))}}
}

// suCommand finds the strings of su -c, --command and --session-command,
// and the -c string among the words after the user, which su hands to
// the user's shell as they are: su root -- -c CMD.
func suCommand(args []string) []piece {
	opts, ops := suOpts.read(args)
	ps := values(opts, "c", "command", "session-command")
	if len(ops) > 0 && args[ops[0]] == "-" {
		ops = ops[1:]
	}
	if len(ops) > 1 {
		shell := make([]string, len(ops)-1)
		for k, w := range ops[1:] {
			shell[k] = args[w]
		}
		if script, _, _ := shellArgs(shell); script >= 0 {
			ps = append(ps, piece{shell[script], ops[1+script : 2+script]})
		}
	}
	return ps
}

// flockCommand finds the string of flock [OPTIONS] FILE -c CMD; the
// command of flock FILE CMD ARGS... is no string.
func flockCommand(args []string) []piece {
	_, ops := flockOpts.read(args)
	if len(ops) < 3 || args[ops[1]] != "-c" && args[ops[1]] != "--command" {
		return nil
	}
	return []piece{{args[ops[2]], ops[2:3]}}
}

func scriptCommand(args []string) []piece {
	opts, _ := scriptOpts.read(args)
	return values(opts, "c", "command")
}

// watchCommand finds the command of watch: its words joined with spaces
// for sh -c, unless -x runs them as they are.
func watchCommand(args []string) []piece {
	opts, ops := watchOpts.read(args)
	if len(ops) == 0 || slices.ContainsFunc(opts, func(o option) bool { return o.name == "x" || o.name == "exec" }) {
		return nil
	}
	return []piece{{strings.Join(args[ops[0]:], " "), ops}}
}

// values are the pieces of the options named names.
func values(opts []option, names ...string) []piece {
	var ps []piece
	for _, o := range opts {
		if slices.Contains(names, o.name) {
			ps = append(ps, piece{o.value, []int{o.word}})
		}
	}
	return ps
}

// getopt is how a command reads its options, in the terms of getopt_long:
// short is its optstring, where a letter with ":" takes a value from the
// rest of its word or the next one, with "::" only from the rest of its
// word, and a leading "+" ends the options at the first operand; long
// lists its long options, with the same colons.
type getopt struct {
	short, long string
}

// option is an option a command has read: its letter or its long name,
// its value and the index of the word that holds the value.
type option struct {
	name, value string
	word        int
}

// read reads args as getopt_long does: the options up to "--", among the
// operands as well unless short starts with "+", and the indexes of the
// operands in order. An unknown option is taken for one with no value:
// the command fails on it anyway.
func (g getopt) read(args []string) (opts []option, operands []int) {
	short, posix := strings.CutPrefix(g.short, "+")
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return opts, append(operands, indexes(i+1, len(args))...)
		case len(a) < 2 || a[0] != '-':
			if posix {
				return opts, append(operands, indexes(i, len(args))...)
			}
			operands = append(operands, i)
		case strings.HasPrefix(a, "--"):
			name, v, eq := strings.Cut(a[2:], "=")
			o := option{value: v, word: i}
			var takes int
			if o.name, takes = g.longOpt(name); takes == 1 && !eq && i+1 < len(args) {
				i++
				o.value, o.word = args[i], i
			}
			opts = append(opts, o)
		default:
			for j := 1; j < len(a); j++ {
				o := option{name: a[j : j+1], word: i}
				if takes := colons(short, a[j]); takes > 0 {
					o.value = a[j+1:]
					if takes == 1 && o.value == "" && i+1 < len(args) {
						i++
						o.value, o.word = args[i], i
					}
					j = len(a)
				}
				opts = append(opts, o)
			}
		}
	}
	return opts, operands
}

// longOpt finds a long option by its name, or by a prefix of the name of
// no other option, and tells the value it takes: 0 none, 1 a value, 2 an
// optional one.
func (g getopt) longOpt(name string) (string, int) {
	var found []string
	for _, l := range strings.Fields(g.long) {
		switch n := strings.TrimRight(l, ":"); {
		case n == name:
			return n, len(l) - len(n)
		case strings.HasPrefix(n, name):
			found = append(found, l)
		}
	}
	if len(found) != 1 {
		return name, 0
	}
	n := strings.TrimRight(found[0], ":")
	return n, len(found[0]) - len(n)
}

// colons tells the value the short option c of optstring takes, as
// longOpt does.
func colons(optstring string, c byte) int {
	i := strings.IndexByte(optstring, c)
	if c == ':' || i < 0 {
		return 0
	}
	rest := optstring[i+1:]
	return len(rest) - len(strings.TrimLeft(rest, ":"))
}

// indexes are the numbers from i up to n, n left out.
func indexes(i, n int) []int {
	var out []int
	for ; i < n; i++ {
		out = append(out, i)
	}
	return out
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

// chdirs run what follows them in another directory.
var chdirs = map[string]bool{"cd": true, "pushd": true, "popd": true}

// redirect records the file a redirection writes. Reading, a copy of a
// descriptor and the devices that are no file are left out; a file built
// at run time is marked. On another machine it writes no file here.
func (p *parser) redirect(r *syntax.Redirect) {
	if p.remote {
		return
	}
	switch r.Op {
	case syntax.RdrOut, syntax.AppOut, syntax.RdrClob, syntax.RdrAll, syntax.AppAll, syntax.RdrInOut:
	case syntax.DplOut:
		// >&2, 2>&1, 3>&- and 4>&3- are descriptors; >&file is &>file.
		if isStatic(r.Word) && descriptor(word(r.Word)) {
			return
		}
	default:
		return
	}
	if isStatic(r.Word) && device(word(r.Word)) {
		return
	}
	path, rel, ok := p.target(r.Word)
	if !ok {
		p.mark(dynComputed)
		p.unknown = true
		return
	}
	p.writes = append(p.writes, write{path, rel})
}

// descriptor tells whether the word of >& names a descriptor: digits, a
// "-" closing it, or digits and a "-" moving it.
func descriptor(s string) bool {
	if s == "-" {
		return true
	}
	return digits(strings.TrimSuffix(s, "-"))
}

func digits(s string) bool {
	return s != "" && strings.Trim(s, "0123456789") == ""
}

// device tells whether a redirection to s writes to no file: /dev/null,
// the terminal or a descriptor of the shell.
func device(s string) bool {
	switch s {
	case "/dev/null", "/dev/stdout", "/dev/stderr", "/dev/tty":
		return true
	}
	fd, ok := strings.CutPrefix(s, "/dev/fd/")
	return ok && digits(fd)
}

// target makes the word of a redirection a path, as the shell expands it:
// a leading ~, $HOME or ${HOME} is home, and $PWD, ${PWD} and a relative
// path are taken from cwd (rel). Not filepath.Join: it would take link/..
// away before the link is followed. Any other expansion, ~user and a glob
// among them, leaves the file unknown.
func (p *parser) target(w *syntax.Word) (path string, rel, ok bool) {
	if len(w.Parts) == 0 {
		return "", false, false
	}
	parts := w.Parts
	var dir string
	found := false
	switch first := parts[0].(type) {
	case *syntax.Lit:
		v, tilde := strings.CutPrefix(first.Value, "~")
		switch {
		case !tilde:
		case v == "" && len(parts) == 1, strings.HasPrefix(v, "/"):
			dir, found = p.home, true
			parts = append([]syntax.WordPart{&syntax.Lit{Value: v}}, parts[1:]...)
		default:
			// ~user is another home, ~+ and ~- are directories of the
			// shell, and a quote in ~"/x" keeps the tilde as it is.
			return "", false, false
		}
	case *syntax.ParamExp:
		if dir, rel, found = p.param(first); found {
			parts = parts[1:]
		}
	case *syntax.DblQuoted:
		if pe, ok := firstParam(first); ok {
			if dir, rel, found = p.param(pe); found {
				parts = append([]syntax.WordPart{&syntax.DblQuoted{Parts: first.Parts[1:]}}, parts[1:]...)
			}
		}
	}
	rest := &syntax.Word{Parts: parts}
	if !isStatic(rest) || found && !rel && dir == "" {
		return "", false, false
	}
	s := word(rest)
	switch {
	case found:
		return dir + s, rel, true
	case filepath.IsAbs(s):
		return s, false, true
	}
	return p.cwd + "/" + s, true, true
}

// param tells the directory $HOME, ${HOME}, $PWD or ${PWD} stands for.
func (p *parser) param(pe *syntax.ParamExp) (dir string, rel, ok bool) {
	var b strings.Builder
	if err := syntax.NewPrinter().Print(&b, pe); err != nil {
		return "", false, false
	}
	switch b.String() {
	case "$HOME", "${HOME}":
		return p.home, false, true
	case "$PWD", "${PWD}":
		return p.cwd, true, true
	}
	return "", false, false
}

func firstParam(q *syntax.DblQuoted) (*syntax.ParamExp, bool) {
	if q.Dollar || len(q.Parts) == 0 {
		return nil, false
	}
	pe, ok := q.Parts[0].(*syntax.ParamExp)
	return pe, ok
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
