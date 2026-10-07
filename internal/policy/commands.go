package policy

import (
	"errors"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"mvdan.cc/sh/v3/syntax"
)

const maxDepth = 4

// Script is what a policy learns about a bash line as a whole.
type Script struct {
	// Commands holds the argv of every simple command in the line: in
	// pipelines, $(...), subshells, behind wrappers, and in the code the
	// line hands to eval, bash -c, a shell's here-string or here-document,
	// env -S, alias, trap, bind -x, complete -C, compgen -C, mapfile -C,
	// su -c, runuser -c, sg, flock -c, script -c, watch, sudo -s and -i,
	// run0 -i, strace -o '|CMD', fakeroot -l, in the value of PAGER and the
	// other commandVars, the runners (tmux, screen, parallel, an alias of
	// git) and, on another machine, ssh HOST CMD or the here-string of ssh
	// HOST. Words that are not static (expansions, substitutions) are kept
	// in their source form, e.g. "$HOME".
	Commands [][]string
	// Remote holds the indexes in Commands of the commands that run on
	// another machine, in the command of ssh: their words name no files
	// of this one, and their redirections are not in Writes.
	Remote []int
	// Dynamic names what in the line runs code the parser cannot see:
	// "computed", "source", "stdin", "prompt", "rebind", "depth". Sorted,
	// no repeats; empty when every command is known.
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
	// sites holds where the line runs each command of Commands, for its
	// paths (see shell.at); nil without a cwd.
	sites []site
}

// The kinds of Script.Dynamic.
const (
	// dynComputed is a program or code made of expansions: $x -rf,
	// "$(which rm)", eval "$x", bash -c "$x"; the name of a variable made
	// of them, read "$v"; code the line does not hold: a bind macro, the
	// history fc runs; a path no policy can know before the line runs (see
	// shell.at, lineVars).
	dynComputed = "computed"
	// dynSource is source or ., whose file may change after the check.
	dynSource = "source"
	// dynStdin is a shell reading its commands from stdin: echo … | bash.
	dynStdin = "stdin"
	// dynPrompt is an assignment to a variable the shell runs later: by
	// =, by declare and its kin, or by a builtin such as read and printf -v.
	dynPrompt = "prompt"
	// dynRebind makes a name of a command run another program or code:
	// hash -p, enable, an assignment to PATH (see rebindVars) or to a
	// variable that has programs load code, LD_PRELOAD (see loaderVars),
	// the code of an alias git config keeps.
	dynRebind = "rebind"
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
// or of the code in it, the script holds the commands before the error,
// which bash runs, and those of the rest of the code in the line; the
// error is the line's own, else that of the first code that fails.
func Parse(src, cwd, home string) (Script, error) {
	return parseIn(src, shell{pwd: cwd, home: home})
}

// parseIn is Parse in the shell sh, whose CDPATH a cd in the line looks
// in.
func parseIn(src string, sh shell) (Script, error) {
	cwd := sh.pwd
	p := &parser{kinds: map[string]bool{}, cwd: cwd, home: sh.home, modes: inShell(sh)}
	if cwd != "" {
		p.line = &lines{sh: sh, at: map[*syntax.CallExpr]where{}}
	}
	err := p.parse(src, 0, false)
	s := Script{Commands: p.out, Remote: p.remotes, sites: p.settle()}
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
	// chdir is a cd, pushd or popd somewhere in the line, or a command
	// that runs another in another directory (see moves).
	chdir bool
	// remote tells that the code being walked runs on another machine,
	// remotes are the indexes in out of the commands that run there.
	remote  bool
	remotes []int
	// varCode is the code of the variables of commandVars the walk of a
	// line assigns: it is parsed after the walk, as that of bash -c is.
	varCode []snippet
	// line follows where the commands of the line run, sites holds it by
	// the index of each in out; nil without a cwd.
	line  *lines
	sites []site
	// evals is the code of the strings bash evaluates as the line runs
	// (see evaluates), kept as the line is walked and parsed after it.
	evals []string
	// modes are where the line runs its commands under set -k and shopt -s
	// cdable_vars (see modesOf).
	modes [numModes]mode
	// prompts follows ${x@P} through the line (see prompt).
	prompts prompts
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
	stmts, err := p.statements(src)
	p.remote = remote
	p.modesOf(stmts, depth)
	if depth == 0 && p.line != nil {
		p.line.track(stmts)
	}
	var nested []snippet
	done := map[*syntax.CallExpr]bool{}
	visit := func(n syntax.Node) bool {
		p.evaluates(n)
		switch n := n.(type) {
		case *syntax.Stmt:
			// The redirections are the statement's: they tell what a shell
			// it runs reads from stdin.
			if call, ok := n.Cmd.(*syntax.CallExpr); ok {
				done[call] = true
				nested = append(nested, p.placed(call, n.Redirs)...)
			}
		case *syntax.CallExpr:
			if !done[n] {
				nested = append(nested, p.placed(n, nil)...)
			}
		case *syntax.DeclClause:
			p.decl(n)
		case *syntax.WordIter:
			p.iter(n)
		case *syntax.ParamExp:
			p.defaulted(n)
			p.prompt(n)
		case *syntax.Redirect:
			// Of any statement: { …; } > f and done > f write too.
			p.redirect(n)
		}
		return true
	}
	for _, s := range stmts {
		syntax.Walk(s, visit)
	}
	nested, p.varCode = append(nested, p.varCode...), nil
	for _, src := range p.takeEvals() {
		nested = append(nested, snippet{src, remote})
	}
	if len(nested) > 0 && depth >= maxDepth {
		// What is not parsed must not pass for checked.
		p.mark(dynDepth)
		return err
	}
	// bash -c "'" fails and the bash -c after it runs all the same: an
	// error in one piece of code leaves the others to be parsed.
	for _, s := range nested {
		if e := p.parse(s.src, depth+1, s.remote); err == nil {
			err = e
		}
	}
	return err
}

// maxReopen is how many here-documents left open statements closes: one
// at a time, as the parser stops at the first.
const maxReopen = 8

// statements parses src as bash runs it: command by command, each one as
// soon as it is whole. On a syntax error the commands before it are
// returned with the error: bash, in eval and bash -c as in a script on
// stdin, has run the lines before the error by then and stops there.
// Those before it on its own line bash does not run; a policy that sees
// them too is only stricter. A here-document left open is no error to
// bash, which ends it at the end of the input with a warning and runs the
// command: it is closed here too, and the error is returned all the same,
// for a policy to know the line did not parse as written. So is a
// construct bash fails in only when it runs it (see arith): it is
// rewritten, and the line marked computed.
func (p *parser) statements(src string) ([]*syntax.Stmt, error) {
	stmts, first := upToError(src)
	err := first
	for range maxReopen + maxArith {
		if stop, open := unclosedHdoc(err); open {
			src += "\n" + stop
			stmts, err = upToError(src)
		} else if r, ok := arith(src, err); ok {
			p.mark(dynComputed)
			src, stmts, err = r.src, r.stmts, r.err
		} else {
			break
		}
	}
	return stmts, first
}

// upToError is the statements of src before its first syntax error.
func upToError(src string) ([]*syntax.Stmt, error) {
	var stmts []*syntax.Stmt
	for s, err := range syntax.NewParser(syntax.Variant(syntax.LangBash)).StmtsSeq(strings.NewReader(src)) {
		if err != nil {
			// A statement that comes with the error is not whole.
			return stmts, err
		}
		stmts = append(stmts, s)
	}
	return stmts, nil
}

// unclosedHdoc tells whether err is of a here-document left open, and the
// line that ends it.
func unclosedHdoc(err error) (string, bool) {
	var pe syntax.ParseError
	if !errors.As(err, &pe) {
		return "", false
	}
	quoted, ok := strings.CutPrefix(pe.Text, "unclosed here-document ")
	if !ok {
		return "", false
	}
	stop, uerr := strconv.Unquote(quoted)
	return stop, uerr == nil && !strings.Contains(stop, "\n")
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
	split := make([]bool, len(call.Args))
	for i, w := range call.Args {
		argv[i], static[i], split[i] = word(w), isStatic(w), splits(w)
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
		if !p.remote && static[0] && moves(argv) {
			p.chdir = true
		}
		here, there, local := p.handed(argv, static, redirs)
		found := p.shellC(argv[:local], static[:local])
		found = append(found, p.runs(argv[:local], static[:local], split[:local], redirs)...)
		found = append(found, p.program(argv, static, redirs)...)
		for _, s := range append(found, here...) {
			add(s, p.remote)
		}
		for _, s := range there {
			add(s, true)
		}
		argv, static, split = p.next(argv, static, split)
	}
	return code
}

// program looks at what argv runs and returns the code it hands to eval,
// alias, trap, one of setters or, on stdin, a shell.
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
	case wrappers[name].hands():
		return p.wrapped(wrappers[name], args, st, redirs)
	case logins[name] != nil:
		if logins[name](args) {
			return p.stdin(redirs)
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
	case setters[name] != nil:
		return setters[name](p, args, st)
	}
	return nil
}

// next returns the command a wrapper in argv runs, with the static and
// split flags of its words; nil when argv runs none.
func (p *parser) next(argv []string, static, split []bool) ([]string, []bool, []bool) {
	name := filepath.Base(argv[0])
	w, ok := wrappers[name]
	if !ok {
		return nil, nil, nil
	}
	cmd, opts := p.unwrap(w, argv, static, split)
	if s := values(opts, "S", "split-string"); name == "env" && len(s) > 0 {
		// env -S has quotes, escapes and ${VAR} of its own: only a
		// string of plain words is split here as env splits it.
		i := 1 + s[0].words[0]
		if !static[i] || strings.ContainsAny(s[0].text, `'"\$`) {
			p.mark(dynComputed)
			return nil, nil, nil
		}
		fields := strings.Fields(s[0].text)
		words := append(append([]string{argv[0]}, fields...), argv[i+1:]...)
		st, sp := make([]bool, len(words)), make([]bool, len(words))
		for k := range st {
			st[k] = true
		}
		copy(st[1+len(fields):], static[i+1:])
		copy(sp[1+len(fields):], split[i+1:])
		return words, st, sp
	}
	if cmd == 0 {
		return nil, nil, nil
	}
	if prog, st := w.programOf(opts, static[1:]); prog != nil {
		// The program stands in the place of the word at cmd.
		argv = append(slices.Clone(prog), argv[cmd+1:]...)
		static = append(slices.Repeat([]bool{st}, len(prog)), static[cmd+1:]...)
		split = append(make([]bool, len(prog)), split[cmd+1:]...)
	} else {
		argv, static, split = argv[cmd:], static[cmd:], split[cmd:]
	}
	if w.fills != nil {
		static, split = slices.Clone(static), slices.Clone(split)
		w.fills(p, opts, argv, static, split)
	}
	return argv, static, split
}

// wrappers run a command made of their words, after options read as their
// getopt calls read them in coreutils 9.11, util-linux 2.42, procps-ng 4,
// findutils 4.11, GNU time 1.9, sudo 1.9, OpenDoas and bash 5: a value of
// an option is no command. Options of other builds are in too (sudo -c,
// -r, xargs -J of BSD): a build that does not know one fails on it. Those
// of other packages are in moreWrappers.
var wrappers = map[string]wrapper{
	"builtin": {opts: getopt{short: "+"}},
	// chroot DIR with no command runs "$SHELL" -i.
	"chroot":  {opts: getopt{short: "+", long: "groups: userspec: skip-chdir help version"}, operands: 1, bare: true},
	"command": {opts: getopt{short: "+pVv"}, none: []string{"v", "V"}},
	"doas":    {opts: getopt{short: "+C:Lnsu:"}, none: []string{"C"}, shell: []string{"s"}},
	"env": {
		opts: getopt{short: "+a:C:iS:u:v0", long: "argv0: ignore-environment null unset: chdir: split-string: block-signal:: default-signal:: ignore-signal:: list-signal-handling debug help version"},
		env:  true, chdir: []string{"C", "chdir"},
	},
	"exec":   {opts: getopt{short: "+cla:"}},
	"flock":  {opts: flockOpts, operands: 1},
	"ionice": {opts: getopt{short: "+c:n:p:P:u:tVh", long: "class: classdata: pid: pgid: uid: ignore help version"}, none: []string{"p", "P", "u", "pid", "pgid", "uid"}},
	"nice":   {opts: getopt{short: "+n:", long: "adjustment: help version"}},
	"nohup":  {opts: getopt{short: "+", long: "help version"}},
	// runuser -u runs its operands; without -u it is su.
	"runuser": {opts: suOpts, only: []string{"u", "user"}},
	"setsid":  {opts: getopt{short: "+cfwVh", long: "ctty fork wait help version"}},
	"stdbuf":  {opts: getopt{short: "+i:o:e:", long: "input: output: error: help version"}},
	// sudo takes the word after a bare -h for the host, unless it starts
	// with - or holds =: then -h asks for help and nothing runs. Either way
	// that word is no command, so h: rather than h::.
	"sudo": {
		opts: getopt{short: "+Aa:BbC:c:D:Eeg:Hh:iKklNnPp:R:r:SsT:t:U:u:Vv", long: "askpass auth-type: background bell close-from: login-class: chdir: preserve-env:: edit group: set-home help host: login remove-timestamp reset-timestamp list no-update non-interactive preserve-groups prompt: chroot: role: stdin shell command-timeout: type: other-user: user: validate version"},
		env:  true, none: []string{"e", "edit", "l", "list"},
		chdir: []string{"D", "chdir", "i", "login", "R", "chroot"}, shell: []string{"s", "shell", "i", "login"},
	},
	"time":    {opts: getopt{short: "+af:o:pqvV", long: "append format: output: portability quiet verbose help version"}},
	"timeout": {opts: getopt{short: "+fk:ps:v", long: "foreground kill-after: preserve-status signal: verbose help version"}, operands: 1},
	// watch without -x joins its words for sh -c: strung reads them.
	"watch": {opts: watchOpts, only: []string{"x", "exec"}},
	// xargs fills in its command: see xargsFills, set by init.
	"xargs": {opts: getopt{short: "+0a:E:e::i::I:J:l::L:n:prR:s:S:txP:d:o", long: "null arg-file: delimiter: eof:: replace:: max-lines:: max-args: max-procs: open-tty interactive no-run-if-empty max-chars: verbose show-limits exit process-slot-var: help version"}},
}

// wrapper is how a wrapper reads its words up to the command it runs.
type wrapper struct {
	opts getopt
	// operands is how many operands come before the command: the
	// duration of timeout, the new root of chroot, the file of flock.
	operands int
	// env takes NAME=VALUE words (and env's "-") after the options for
	// the environment of the command; sudo reads options again after
	// them, env takes the next word for the command and fails on an
	// option.
	env bool
	// only lists the options without which its operands are no command,
	// none those with which they are none: command -v, sudo -l, ionice -p.
	only, none []string
	// chdir lists the options that run the command in another directory,
	// shell those that run a shell instead, which takes the words of the
	// command for its -c and, with none, its commands from stdin.
	chdir, shell []string
	// stays lists the options that keep the command in this directory when
	// without them the wrapper runs it in another: pkexec in the home of
	// the user, systemd-run a service in that of its manager.
	stays []string
	// joins tells that the shell of shell gets the words joined with
	// spaces, as ssh joins them (run0 --via-shell), not escaped.
	joins bool
	// bare tells that with no command the wrapper runs a shell, which reads
	// its commands from stdin: chroot DIR, unshare, pkexec.
	bare bool
	// attach lists the options without which a bare wrapper runs no shell
	// on stdin: docker run -i IMAGE, machinectl shell.
	attach []string
	// prog makes the program the wrapper runs in place of the word at the
	// index of its command, and whether it is static; nil for that word:
	// capsh -- runs its shell, docker run --entrypoint the entrypoint.
	prog func(opts []option, static []bool) ([]string, bool)
	// reads reads the words in place of opts when getopt alone does not
	// tell where the command is: the priority of chrt is one only when it
	// is a number, the architecture of setarch comes before its options.
	reads func(args []string) (opts []option, cmd int)
	// check looks at the values of the options for the code the wrapper
	// runs besides the command, which it returns, and the variables it
	// sets: strace -o '|CMD', systemd-run -p ExecStartPre=…, -E PATH=….
	check func(p *parser, opts []option, args []string, static []bool, cmd int) []string
	// fills marks the words of the command the wrapper fills in when it
	// runs (with the replace string of xargs -I, the $NAME systemd-run
	// expands) as made at run time, and what xargs appends to them.
	fills func(p *parser, opts []option, argv []string, static, split []bool)
}

// read reads args as the wrapper does: its options, and the index in args
// of the command, len(args) for none.
func (w wrapper) read(args []string) (opts []option, cmd int) {
	if w.reads != nil {
		return w.reads(args)
	}
	for {
		o, ops := w.opts.read(args[cmd:])
		for _, x := range o {
			x.word += cmd
			opts = append(opts, x)
		}
		if len(ops) == 0 {
			return opts, len(args)
		}
		from := cmd + ops[0]
		cmd = from
		for w.env && cmd < len(args) && (args[cmd] == "-" || strings.Contains(args[cmd], "=")) {
			cmd++
		}
		if cmd == from {
			return opts, min(cmd+w.operands, len(args))
		}
	}
}

// fixed tells whether what makes a word a wrapper reads an option, which
// one and whether its value is in the word, or a NAME=VALUE, is text the
// shell keeps as it is. -u"$u" is not: empty, it takes the next word.
func (w wrapper) fixed(a string) bool {
	head := a
	switch short := strings.TrimPrefix(w.opts.short, "+"); {
	case strings.HasPrefix(a, "--"), w.env && !strings.HasPrefix(a, "-"):
		head, _, _ = strings.Cut(a, "=")
	case strings.HasPrefix(a, "-"):
		for j := 1; j < len(a); j++ {
			if colons(short, a[j]) > 0 {
				head = a[:min(j+2, len(a))]
				break
			}
		}
	}
	return !strings.ContainsAny(head, "$`\\")
}

// unwrap reads argv as its wrapper w does and returns the index in argv of
// the command it runs, 0 for none, and the options it read. The words the
// wrapper reads itself are marked when the shell may make other words of
// them, which moves the command among them: split or globbed, or not fixed.
// A value of an option stays one word whatever it holds.
func (p *parser) unwrap(w wrapper, argv []string, static, split []bool) (int, []option) {
	args := argv[1:]
	opts, cmd := w.read(args)
	if w.only != nil && !has(opts, w.only...) {
		return 0, opts
	}
	own := cmd
	for _, o := range opts {
		if slices.Contains(w.none, o.name) {
			// After it no word makes the wrapper run a command.
			own = min(own, o.word)
		}
	}
	for i, a := range args[:own] {
		if split[1+i] || !static[1+i] && !valued(opts, args, i) && !w.fixed(a) {
			p.mark(dynComputed)
		}
	}
	switch {
	case own < cmd, cmd == len(args), has(opts, w.shell...):
		return 0, opts
	case filepath.Base(argv[0]) == "flock" && (args[cmd] == "-c" || args[cmd] == "--command"):
		// flock FILE -c CMD runs a string: strung reads it.
		return 0, opts
	}
	return 1 + cmd, opts
}

// wrapped looks at what a wrapper hands the command it runs besides its
// words: the environment of env and sudo, the code in the values of its
// options (check), and the shell of sudo -s, -i and doas -s, which gets
// those words for its -c as escaped puts them (or joined, run0 -i) and,
// with none of them, reads its commands from stdin, as the shell of a
// bare wrapper with no command does.
func (p *parser) wrapped(w wrapper, args []string, static []bool, redirs []*syntax.Redirect) []string {
	opts, cmd := w.read(args)
	for i, a := range args[:cmd] {
		switch name, value, ok := strings.Cut(a, "="); {
		case !ok || valued(opts, args, i):
		case static[i]:
			p.assignedTo(name, value)
		default:
			p.assigned(name)
			// The value is read as that of x=… is (see assignedTo): what is
			// written out in it, the \x24( of $'…' decoded, as bash does in
			// env $'x=a[\x24(id)]' bash -c '((x))'.
			p.subscript(ansiC(value))
		}
	}
	var code []string
	if w.check != nil {
		code = w.check(p, opts, args, static, cmd)
	}
	shell := has(opts, w.shell...)
	bare := w.bare && (w.attach == nil || has(opts, w.attach...))
	switch {
	case has(opts, w.none...), !shell && (!bare || cmd < len(args)):
		return code
	case cmd == len(args):
		return append(code, p.stdin(redirs)...)
	case w.joins && slices.Contains(static[cmd:], false), !w.joins && !static[cmd]:
		// Joined, a word made at run time may be any code; escaped, the
		// other words are its arguments whatever they hold.
		p.mark(dynComputed)
		return code
	case w.joins:
		return append(code, strings.Join(args[cmd:], " "))
	}
	return append(code, escaped(args[cmd:]))
}

// escaped joins words with spaces as sudo and sudo-rs do for the -c of the
// shell of -s and -i: every character but an ASCII letter or digit, _, -
// and $ behind a backslash. Each word stays one, and only $NAME expands
// there: sudo -s 'rm -rf /' runs no rm.
func escaped(words []string) string {
	var b strings.Builder
	for i, w := range words {
		if i > 0 {
			b.WriteByte(' ')
		}
		for len(w) > 0 {
			r, n := utf8.DecodeRuneInString(w)
			plain := r == '_' || r == '-' || r == '$' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
			if !plain {
				b.WriteByte('\\')
			}
			b.WriteString(w[:n])
			w = w[n:]
		}
	}
	return b.String()
}

// valued tells whether word i of args is the value of an option, whole.
func valued(opts []option, args []string, i int) bool {
	return slices.ContainsFunc(opts, func(o option) bool { return o.word == i && o.value == args[i] })
}

// has tells whether opts holds an option of one of names.
func has(opts []option, names ...string) bool {
	return slices.ContainsFunc(opts, func(o option) bool { return slices.Contains(names, o.name) })
}

// shells take their commands from -c, a file or stdin.
var shells = map[string]bool{"bash": true, "sh": true, "zsh": true, "dash": true}

// shellC returns the scripts of `bash -c SCRIPT`, `sh -c`, … wherever a
// shell is in argv: behind sudo or env, and as an argument of find -exec
// or anything else that may run it. A script built at run time is marked;
// a mode its options start the shell in is followed (see started).
func (p *parser) shellC(argv []string, static []bool) []string {
	var code []string
	for i, a := range argv {
		if !shells[filepath.Base(a)] {
			continue
		}
		p.started(argv[i+1:], static[i+1:])
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
	"ssh":     {sshCommand, true},
	"su":      {suCommand, false},
	"sg":      {sgCommand, false},
	"runuser": {runuserCommand, false},
	"flock":   {flockCommand, false},
	"script":  {scriptCommand, false},
	"watch":   {watchCommand, false},
}

// piece is a string a command runs through a shell, and the indexes of
// the words of its arguments the string is made of.
type piece struct {
	text  string
	words []int
}

// handed returns the strings the commands of strung in argv run, wherever
// they are in it, as shellC finds shells: here those of su -c, runuser -c,
// flock -c, script -c and watch, there the command of ssh and, when ssh
// that is the program has none, what the redirections of its statement
// give the shell over there on stdin. A string with a word built at run
// time is marked. As the program of argv such a command is marked for a
// word of that kind anywhere, which word splitting may make an option, the
// host or a string. local is how many words of argv run here: after an ssh
// that is the program, none of them is a shell of this machine.
func (p *parser) handed(argv []string, static []bool, redirs []*syntax.Redirect) (here, there []string, local int) {
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
			if sshReads(argv[1:]) {
				there = append(there, p.stdin(redirs)...)
			}
			return here, there, 1
		}
	}
	return here, there, len(argv)
}

// sshReads tells whether ssh runs a shell over there that reads its
// commands from stdin: it has a host and no command, no -n or -f keeps
// stdin from the shell and no -N, -W, -O, -G, -V or -Q runs none.
func sshReads(args []string) bool {
	opts, ops := sshOpts.read(args)
	if len(ops) == 0 || sshCommand(args) != nil {
		return false
	}
	if host := ops[0]; host == 0 || args[host-1] != "--" {
		more, _ := sshOpts.read(args[host+1:])
		opts = append(opts, more...)
	}
	return !has(opts, "n", "f", "N", "W", "O", "G", "V", "Q")
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

// runuserCommand finds the strings of runuser as suCommand does those of
// su; runuser -u runs its operands as a command, a wrapper.
func runuserCommand(args []string) []piece {
	if opts, _ := suOpts.read(args); has(opts, "u", "user") {
		return nil
	}
	return suCommand(args)
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

// chdirs run what follows them in another directory, chroot its command.
var chdirs = map[string]bool{"cd": true, "pushd": true, "popd": true, "chroot": true}

// moves tells whether argv runs what follows it, or the command it runs,
// in another directory: as chdirs do, a wrapper with an option of chdir
// (env -C, sudo -D, sudo -i) or without one of stays (pkexec), and su or
// runuser starting a login shell, which starts in the home of the user.
func moves(argv []string) bool {
	name := filepath.Base(argv[0])
	switch {
	case chdirs[name]:
		return true
	case name == "su" || name == "runuser":
		opts, ops := suOpts.read(argv[1:])
		return has(opts, "l", "login") || len(ops) > 0 && argv[1+ops[0]] == "-"
	}
	w, ok := wrappers[name]
	if !ok {
		return false
	}
	opts, _ := w.read(argv[1:])
	return has(opts, w.chdir...) || w.stays != nil && !has(opts, w.stays...)
}

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

// promptVars hold code the shell runs later, outside any check: at every
// prompt (MAILPATH's messages are expanded when mail comes), or in every
// bash or sh it starts.
var promptVars = map[string]bool{
	"PROMPT_COMMAND": true, "PS0": true, "PS1": true, "PS2": true, "PS4": true,
	"BASH_ENV": true, "ENV": true, "MAILPATH": true,
}

// rebindVars tell which program or code a name of a command runs: PATH
// and EXECIGNORE where it is looked for, BASH_CMDS is the table of hash
// and BASH_ALIASES that of alias.
var rebindVars = map[string]bool{
	"PATH": true, "EXECIGNORE": true, "BASH_CMDS": true, "BASH_ALIASES": true,
}

// assigned marks an assignment to the variable name of a value not known
// before the line runs: one of commandVars runs code made at run time, one
// of optionVars may start a shell in any mode. One of lineVars changes the
// paths after it whatever its value.
func (p *parser) assigned(name string) {
	switch {
	case promptVars[name]:
		p.mark(dynPrompt)
	case rebindVars[name], loads(name):
		p.mark(dynRebind)
	case commandVars[name], lineVars[name], optionVars[name]:
		p.mark(dynComputed)
	}
}

// named marks an assignment to the variable a word names as text: NAME or
// NAME[SUBSCRIPT], whose subscript bash expands, $(…) and all.
func (p *parser) named(s string) {
	name, sub, ok := strings.Cut(s, "[")
	if ok && strings.ContainsAny(sub, "$`") {
		p.mark(dynComputed)
	}
	p.subscript(s)
	p.assigned(name)
}

func (p *parser) assign(a *syntax.Assign) {
	if a.Name != nil && !a.Naked {
		p.assignment(a)
	}
}

// decl looks at the assignments of export, declare, local, readonly and
// typeset. Their arguments assign even when quoted whole, as in
// `export 'PS1=$(id)'`, and declare -n r=PS1 makes r another name of PS1.
func (p *parser) decl(d *syntax.DeclClause) {
	export := d.Variant.Value == "export"
	nameref := d.Variant.Value == "nameref"
	for _, a := range d.Args {
		switch {
		case a.Name != nil:
			p.assign(a)
			switch {
			case !nameref || a.Value == nil:
			case !isStatic(a.Value):
				// It may name any variable.
				p.mark(dynComputed)
			default:
				p.named(word(a.Value))
			}
		case a.Value == nil:
		case !isStatic(a.Value):
			p.mark(dynComputed)
		default:
			p.declWord(word(a.Value), export, &nameref)
		}
	}
}

// declWord looks at a word of export, declare, local, readonly or typeset
// that the parser keeps as text: an option, with which -n makes the names
// namerefs, or a NAME=VALUE quoted whole or of the builtin run as a
// command.
func (p *parser) declWord(v string, export bool, nameref *bool) {
	if strings.HasPrefix(v, "-") {
		*nameref = *nameref || !export && strings.Contains(v, "n")
		return
	}
	name, value, ok := strings.Cut(v, "=")
	if !ok {
		return
	}
	p.declared(name, value)
	if *nameref {
		p.named(value)
	}
	p.declValue(value)
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

// splits tells whether a word may become other than one word: an
// expansion out of double quotes is split and globbed, and so is a glob or
// braces; in them "$@", "${a[@]}" and ${!x} (which may name @) become as
// many words as there are elements, none among them.
func splits(w *syntax.Word) bool {
	for _, p := range w.Parts {
		switch p := p.(type) {
		case *syntax.Lit:
			if expands(p.Value) {
				return true
			}
		case *syntax.SglQuoted:
		case *syntax.DblQuoted:
			for _, q := range p.Parts {
				pe, ok := q.(*syntax.ParamExp)
				if !ok {
					continue
				}
				var b strings.Builder
				if pe.Excl || syntax.NewPrinter().Print(&b, pe) != nil || strings.Contains(b.String(), "@") {
					return true
				}
			}
		default:
			return true
		}
	}
	return false
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
