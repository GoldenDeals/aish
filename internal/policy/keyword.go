package policy

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"

	"mvdan.cc/sh/v3/syntax"
)

// Two options of the shell change what bash makes of the words of a line
// past their syntax. Under set -k (set -o keyword, shopt -so keyword) every
// NAME=VALUE word of a simple command, not only one before its name, is an
// assignment to the environment of the command, and bash runs the command
// without those words: set -k; timeout 5 A=B sudo ls runs sudo ls with A
// set, git fetch GIT_SSH_COMMAND='sudo ls' has git run sudo. Under shopt -s
// cdable_vars, cd NAME with no directory NAME to enter goes to the one the
// variable NAME holds. A line that turns them on runs its commands after
// that in them: the parser follows them through the line (see modesOf).
//
// Two more change the code bash reads. With set -o history and set -H on,
// each line it reads goes through history expansion first: !!:s/x/s/ is
// the line before with x made s, so is ^x^s at the start of a line, and
// histchars may make them any characters: no word of the code is surely
// what the parser sees. eval and bash -c read their code with history off,
// whatever the options: the line, which the user's shell runs by eval, is
// out of the mode until a set -o history in it, and so is the script of
// bash -c; a shell that reads its commands from stdin or a file starts in
// it under -i, -o history and -H, or with them in SHELLOPTS. With shopt -u
// interactive_comments an interactive shell, as the user's is, in eval
// too, reads # as any other character: echo A # ; sudo ls runs sudo. Code
// that may be read under history expansion, and code with a comment that
// may be read without comments, is computed (see misreads).
//
// $"…" is translated by the .mo files of TEXTDOMAIN under TEXTDOMAINDIR in
// the locale of the shell, and the translation expanded as "…" is unless
// shopt -s noexpand_translation: the $(…) of a file runs. The shell may
// hold both without exporting them: a $"…" is computed wherever it is.

// The modes of the shell a line may run its commands in, by their index in
// parser.modes.
const (
	keywordMode  = iota // set -k
	cdableMode          // shopt -s cdable_vars
	histMode            // set -o history and set -H: history expansion
	commentsMode        // shopt -u interactive_comments: # starts no comment
	numModes
)

// mode is where a line runs its commands in a mode of the shell.
type mode struct {
	// on holds the commands of the line that may run in the mode.
	on map[*syntax.CallExpr]bool
	// ever tells that the line turns the mode on somewhere: the bodies of
	// its functions and the code it hands to a shell may run in it. So
	// does a shell the line starts in the mode (see startsIn, started).
	ever bool
	// shell tells that the shell is in the mode before the line runs: its
	// options have it on (see shellModes). Of histMode, that the shells
	// the line starts take it from SHELLOPTS: eval runs the line out of it.
	shell bool
}

func (m *mode) set(call *syntax.CallExpr, on bool) {
	if !on {
		return
	}
	if m.on == nil {
		m.on = map[*syntax.CallExpr]bool{}
	}
	m.on[call] = true
}

// turn is what a command does to a mode.
type turn int

const (
	keeps    turn = iota
	turnsOn       // turns it on, or may: a word made at run time may be any option
	turnsOff      // turns it off, and nothing else of it
)

// modesOf follows the modes through the statements of code the parser is
// about to walk, at depth. A command runs in a mode after a command that
// turns it on, in the order of the line, and out of it again after one of
// the line's own statements that turns it off: one in a list, a branch, a
// subshell or a function may not run, or run first. The substitutions in
// the words and redirections of a command run before it. The order does
// not hold for a mode turned on in a loop, whose next turn runs the
// commands before it in the mode, or in a function, which may be called
// before them: the whole line runs in the mode then. A function's body
// runs in a mode the line turns on anywhere, and so does the code the line
// hands to a shell, which the parser walks after the line. A line the
// shell runs in a mode already runs in it from its start. A mode such
// code turns on the walk of the line has not followed, and one the line
// leaves on the lines after it run in, which no check of theirs knows
// unless the shell was in it before: either is marked computed. So is src,
// the code, where bash may read it as other code in a mode (see misreads),
// and a $"…" in it.
func (p *parser) modesOf(src string, stmts []*syntax.Stmt, depth int) {
	// A step is a command entered, which runs in the modes the line is in
	// then, or its statement left, which turns them.
	type step struct {
		call             *syntax.CallExpr
		turns            [numModes]turn
		left             bool
		inFunc, deferred bool
	}
	top := map[*syntax.CallExpr]bool{}
	if depth == 0 {
		for _, s := range stmts {
			if c, ok := s.Cmd.(*syntax.CallExpr); ok && !s.Background && !s.Coprocess {
				top[c] = true
			}
		}
	}
	var steps []step
	var stack []syntax.Node
	funcs, loops := 0, 0
	for _, s := range stmts {
		// Walk calls f(nil) when it leaves a node.
		syntax.Walk(s, func(n syntax.Node) bool {
			in := 1
			if n != nil {
				stack = append(stack, n)
			} else {
				n, stack, in = stack[len(stack)-1], stack[:len(stack)-1], -1
			}
			switch n := n.(type) {
			case *syntax.FuncDecl:
				funcs += in
			case *syntax.WhileClause, *syntax.ForClause:
				loops += in
			case *syntax.CallExpr:
				if in > 0 {
					steps = append(steps, step{call: n, inFunc: funcs > 0, deferred: funcs+loops > 0})
				}
			case *syntax.Stmt:
				// The redirections of its statement are expanded before
				// the command runs.
				if call, ok := n.Cmd.(*syntax.CallExpr); ok && in < 0 {
					steps = append(steps, step{call: call, turns: turns(call), left: true, inFunc: funcs > 0, deferred: funcs+loops > 0})
				}
			case *syntax.DblQuoted:
				if n.Dollar && in > 0 {
					p.mark(dynComputed)
				}
			}
			return true
		})
	}
	for m := range p.modes {
		md := &p.modes[m]
		on, all := false, false
		for _, s := range steps {
			if s.turns[m] == turnsOn {
				on = true
				all = all || s.deferred
			}
		}
		own := md.shell && m != histMode
		if depth == 0 {
			md.ever = on || own
		}
		if (on || own || depth > 0 && md.ever) && misreads(m, src) {
			p.mark(dynComputed)
		}
		state := all || own || depth > 0 && md.ever
		for _, s := range steps {
			switch {
			case s.inFunc && !s.left:
				md.set(s.call, md.ever || on)
			case s.inFunc:
			case !s.left:
				md.set(s.call, state)
			case s.turns[m] == turnsOn:
				state = true
			case s.turns[m] == turnsOff && top[s.call] && !all:
				state = false
			}
		}
		switch {
		case !on:
		case depth == 0 && state && !own:
			p.mark(dynComputed)
		case depth > 0 && !p.remote:
			p.mark(dynComputed)
			md.ever = true
		}
	}
	if depth == 0 && p.line != nil {
		p.line.cdable = p.modes[cdableMode].on
	}
}

// misreads tells whether bash, reading src in the mode m, may run other
// code than the parser reads in it: any under history expansion, one with
// a comment where # starts none.
func misreads(m int, src string) bool {
	switch m {
	case histMode:
		return true
	case commentsMode:
		return commented(src)
	}
	return false
}

// commented tells whether src has a comment: a # the parser takes for the
// start of one. Up to the first, src reads as it does without comments,
// and so does src that fails to parse before it, where bash stops too; a
// # in src that fails to parse is taken for one, wherever it is.
func commented(src string) bool {
	if !strings.Contains(src, "#") {
		return false
	}
	f, err := syntax.NewParser(syntax.KeepComments(true), syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(src), "")
	found := err != nil
	if f != nil && !found {
		syntax.Walk(f, func(n syntax.Node) bool {
			_, c := n.(*syntax.Comment)
			found = found || c
			return !found
		})
	}
	return found
}

// allOn is what a word made at run time among the options of set or shopt
// may do: turn any mode on.
func allOn() (t [numModes]turn) {
	for m := range t {
		t[m] = turnsOn
	}
	return t
}

// turns tells what call does to each mode, as the builtin it runs: set or
// shopt, by itself or behind builtin and command.
func turns(call *syntax.CallExpr) (t [numModes]turn) {
	argv, static := pastBuiltin(callWords(call))
	if len(argv) == 0 || !static[0] {
		return t
	}
	switch argv[0] {
	case "set":
		t, _ = setTurns(argv[1:], static[1:])
	case "shopt":
		t = shoptTurns(argv[1:], static[1:])
	}
	return t
}

// setTurns reads the words of set as bash 5.3 does for the modes: options
// up to "-", "--" or the first word that starts with neither - nor +, an o
// in them taking the next word for the name of an option unless that is
// empty or starts with one of them. A word made at run time among them may
// be any option (computed): it may turn any mode on. Turned on and off in
// one set, a mode is on: set stops at an option it does not know, with
// the options before it set.
func setTurns(args []string, static []bool) (t [numModes]turn, computed bool) {
	var on, off [numModes]bool
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !static[i] {
			return allOn(), true
		}
		if a == "-" || a == "--" || a == "" || a[0] != '-' && a[0] != '+' {
			break
		}
		for _, c := range a[1:] {
			var o *modeOpt
			switch {
			case c != 'o':
				o = flagged(c)
			case i+1 == len(args):
			case !static[i+1]:
				return allOn(), true
			case args[i+1] != "" && args[i+1][0] != '-' && args[i+1][0] != '+':
				i++
				o = named(args[i], false)
			}
			switch {
			case o == nil:
			case (a[0] == '-') != o.off:
				on[o.mode] = true
			default:
				off[o.mode] = true
			}
		}
	}
	for m := range t {
		switch {
		case on[m]:
			t[m] = turnsOn
		case off[m]:
			t[m] = turnsOff
		}
	}
	return t, false
}

// shoptTurns reads the words of shopt as bash does: its options -s, -u,
// -o (with which the names are those of set -o), -p and -q up to the first
// word that is none, then the names of options. A word made at run time
// among the options may be any of them and names, among the names any
// name: it may turn any mode on that the option, set or unset, puts the
// shell in.
func shoptTurns(args []string, static []bool) (t [numModes]turn) {
	set, unset, o := false, false, false
	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		if !static[i] {
			return allOn()
		}
		if a == "--" {
			i++
			break
		}
		if len(a) < 2 || a[0] != '-' {
			break
		}
		set = set || strings.Contains(a, "s")
		unset = unset || strings.Contains(a, "u")
		o = o || strings.Contains(a, "o")
	}
	if set == unset {
		// Neither lists the options; both is an error.
		return t
	}
	for j := i; j < len(args); j++ {
		for _, opt := range modeOpts {
			switch {
			case opt.shopt == o:
				// -o names the options of set -o, else those of shopt.
			case !static[j] && set != opt.off, args[j] == opt.name && set != opt.off:
				t[opt.mode] = turnsOn
			case static[j] && args[j] == opt.name && t[opt.mode] != turnsOn:
				t[opt.mode] = turnsOff
			}
		}
	}
	return t
}

// set marks a word of its options made at run time: it may be -k, or any
// other option that changes what the line runs after it.
func (p *parser) set(args []string, static []bool) []string {
	if _, computed := setTurns(args, static); computed {
		p.mark(dynComputed)
	}
	return nil
}

// keywordCall is call as bash runs it under set -k, where the line may run
// it so: the NAME=VALUE words after its name are assignments to its
// environment, as those before it, and bash takes them out of its words.
// nil when the line does not run it in the mode, or it has no such words.
// The tracker follows the words of a cd as written: one with such words
// is marked.
func (p *parser) keywordCall(call *syntax.CallExpr) *syntax.CallExpr {
	if !p.modes[keywordMode].on[call] || len(call.Args) < 2 {
		return nil
	}
	kw := &syntax.CallExpr{Args: []*syntax.Word{call.Args[0]}}
	for _, w := range call.Args[1:] {
		if a := p.keywordAssign(w); a != nil {
			kw.Assigns = append(kw.Assigns, a)
			p.evaluates(a)
		} else {
			kw.Args = append(kw.Args, w)
		}
	}
	if len(kw.Assigns) == 0 {
		return nil
	}
	if changesDir(call) || changesDir(kw) {
		p.mark(dynComputed)
	}
	return kw
}

// changesDir tells whether call may change the directory of the shell:
// cd, pushd or popd, by itself or behind builtin and command, or a command
// built at run time.
func changesDir(call *syntax.CallExpr) bool {
	argv, static := pastBuiltin(callWords(call))
	return len(argv) > 0 && (!static[0] || chdirsShell(argv[0]))
}

// assignStart is how a word bash may take for an assignment starts: a
// name, then =, += or a subscript.
var assignStart = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\+?=|\[)`)

// keywordAssign is the assignment bash takes the word w for under set -k:
// NAME=VALUE, NAME+=VALUE or NAME[SUBSCRIPT]=VALUE with the name unquoted,
// as the parser takes such a word before the name of a command. nil for
// any other word; one bash may take for an assignment and the parser not
// is marked.
func (p *parser) keywordAssign(w *syntax.Word) *syntax.Assign {
	// The parser cuts a[0]=x into a and [0]=x.
	var head strings.Builder
	for _, part := range w.Parts {
		lit, ok := part.(*syntax.Lit)
		if !ok {
			break
		}
		head.WriteString(lit.Value)
	}
	if !assignStart.MatchString(head.String()) {
		return nil
	}
	var b strings.Builder
	if err := syntax.NewPrinter().Print(&b, w); err != nil {
		p.mark(dynComputed)
		return nil
	}
	src := b.String()
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(src), "")
	if err == nil && len(f.Stmts) == 1 {
		if c, ok := f.Stmts[0].Cmd.(*syntax.CallExpr); ok && len(c.Args) == 0 && len(c.Assigns) == 1 {
			return c.Assigns[0]
		}
	}
	// a[0]x is a word: bash wants = or += right after the subscript.
	if !strings.HasSuffix(assignStart.FindString(src), "[") || strings.Contains(src, "]=") || strings.Contains(src, "]+=") {
		p.mark(dynComputed)
	}
	return nil
}

// byName tells whether cd w, under shopt -s cdable_vars, may enter the
// directory the variable w holds: w is a name, and of the places cd looks
// for it in (see lookup) none is a directory it may enter.
func (sh shell) byName(w string) bool {
	if !syntax.ValidName(w) {
		return false
	}
	if !filepath.IsAbs(sh.pwd) {
		return true
	}
	for _, d := range sh.lookup(w) {
		// 1 is X_OK: cd into a directory it may not search fails.
		if st, err := os.Stat(d); err == nil && st.IsDir() && syscall.Access(d, 1) == nil {
			return false
		}
	}
	return true
}

// modeOpt is an option of the shell that puts it in a mode, by the name
// set -o or shopt has for it.
type modeOpt struct {
	mode  int
	name  string
	shopt bool
	// off tells that the shell is in the mode with the option off.
	off bool
	// flag is the letter of set and bash for the option, 0 for none.
	flag rune
}

// modeOpts are the options of the modes, with the variable a bash takes
// each from when it starts: those of set -o from SHELLOPTS, those of shopt
// from BASHOPTS, lists of names split at colons. bash keeps both readonly,
// and exported they hold the options it has on: they start no shell in a
// mode of an option off.
var modeOpts = []modeOpt{
	{mode: keywordMode, name: "keyword", flag: 'k'},
	{mode: cdableMode, name: "cdable_vars", shopt: true},
	{mode: histMode, name: "history"},
	{mode: histMode, name: "histexpand", flag: 'H'},
	{mode: commentsMode, name: "interactive-comments", off: true},
	{mode: commentsMode, name: "interactive_comments", shopt: true, off: true},
}

// from is the variable a bash takes the option from when it starts.
func (o modeOpt) from() string {
	if o.shopt {
		return "BASHOPTS"
	}
	return "SHELLOPTS"
}

// is tells whether name is the option, as set -o or shopt names it.
func (o modeOpt) is(name string) bool {
	return strings.ReplaceAll(name, "-", "_") == strings.ReplaceAll(o.name, "-", "_")
}

// flagged is the option of a mode the letter c of set and bash stands for;
// nil for none.
func flagged(c rune) *modeOpt {
	for i := range modeOpts {
		if modeOpts[i].flag == c {
			return &modeOpts[i]
		}
	}
	return nil
}

// named is the option of a mode named name, of shopt or of set -o; nil for
// none.
func named(name string, shopt bool) *modeOpt {
	for i := range modeOpts {
		if modeOpts[i].shopt == shopt && modeOpts[i].name == name {
			return &modeOpts[i]
		}
	}
	return nil
}

// optionVars are the variables of modeOpts. bash refuses an assignment to
// them, but env, sudo and a shell other than bash pass them on all the
// same: env SHELLOPTS=keyword bash -c 'git fetch GIT_SSH_COMMAND="sudo ls"'
// runs sudo. A value made at run time may start a shell in any mode.
var optionVars = map[string]bool{"SHELLOPTS": true, "BASHOPTS": true}

// shellModes are the modes of a shell whose options on are opts, by name,
// nil when not known, or that started with env: a bash takes its options
// from SHELLOPTS and BASHOPTS there, and so do those it starts once they
// are exported. A list that leaves out an option has it off. histMode is
// that of the shells the line starts, from SHELLOPTS (see mode.shell).
func shellModes(env, opts []string) (modes [numModes]bool) {
	for _, o := range modeOpts {
		list := getenv(env, o.from())
		listed := slices.Contains(strings.Split(list, ":"), o.name)
		switch {
		case o.off:
			modes[o.mode] = modes[o.mode] || opts != nil && !slices.ContainsFunc(opts, o.is) || list != "" && !listed
		case o.mode == histMode:
			modes[o.mode] = modes[o.mode] || listed
		default:
			modes[o.mode] = modes[o.mode] || slices.Contains(opts, o.name) || listed
		}
	}
	return modes
}

// inShell is the modes of the parser of a line the shell sh runs.
func inShell(sh shell) (modes [numModes]mode) {
	for m, on := range sh.modes {
		modes[m].shell = on
	}
	return modes
}

// startsIn looks at an assignment of value, static, to the variable name:
// one of optionVars starts a bash in the modes of the options it lists,
// and the code the line hands to a shell runs in them, as in one the line
// turns on itself (see modesOf). It tells whether name is one.
func (p *parser) startsIn(name, value string) bool {
	if !optionVars[name] {
		return false
	}
	for _, o := range modeOpts {
		if o.from() == name && !o.off && slices.Contains(strings.Split(value, ":"), o.name) {
			p.modes[o.mode].ever = true
		}
	}
	return true
}

// started looks at the words args after the name of a shell, its options
// as bash reads them up to its first operand: -k, -o keyword and -O
// cdable_vars start it in a mode, and the code it is handed runs in it, as
// in one the line turns on itself. So does +O interactive_comments for a
// shell with code the parser reads, of -c or stdin, and -i, -H and -o
// history, or SHELLOPTS of the line's environment that lists them, for one
// that reads its commands from stdin: one with -c runs its script as eval
// does, with history off. The code of a file is not read: its modes are of
// no other code. A word made at run time among them may be any option.
func (p *parser) started(args []string, static []bool) {
	script, _, stdin := shellArgs(args)
	on := func(m int) {
		switch {
		case m == histMode && !stdin, m == commentsMode && !stdin && script < 0:
		default:
			p.modes[m].ever = true
		}
	}
	if p.modes[histMode].shell {
		on(histMode)
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case !static[i]:
			for m := range p.modes {
				on(m)
			}
			return
		case a == "--" || a == "-":
			return
		case a == "--rcfile" || a == "--init-file":
			i++
		case strings.HasPrefix(a, "--"):
		case len(a) > 1 && (a[0] == '-' || a[0] == '+'):
			for _, r := range a[1:] {
				if o := flagged(r); o != nil && (a[0] == '-') != o.off {
					on(o.mode)
				}
				if r == 'i' && a[0] == '-' {
					// Interactive, it has history expansion on.
					on(histMode)
				}
				if r != 'o' && r != 'O' || i+1 == len(args) {
					continue
				}
				// -o takes a name of set -o, -O one of shopt.
				i++
				for _, o := range modeOpts {
					if (a[0] == '-') != o.off && o.shopt == (r == 'O') && (!static[i] || args[i] == o.name) {
						on(o.mode)
					}
				}
			}
		default:
			return
		}
	}
}
