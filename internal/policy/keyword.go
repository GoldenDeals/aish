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

// The modes of the shell a line may run its commands in, by their index in
// parser.modes.
const (
	keywordMode = iota // set -k
	cdableMode         // shopt -s cdable_vars
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
	// options have it on (see shellModes).
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
// unless the shell was in it before: either is marked computed.
func (p *parser) modesOf(stmts []*syntax.Stmt, depth int) {
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
		if depth == 0 {
			md.ever = on || md.shell
		}
		state := all || md.shell || depth > 0 && md.ever
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
		case depth == 0 && state && !md.shell:
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

// turns tells what call does to each mode, as the builtin it runs: set or
// shopt, by itself or behind builtin and command.
func turns(call *syntax.CallExpr) (t [numModes]turn) {
	argv, static := pastBuiltin(callWords(call))
	if len(argv) == 0 || !static[0] {
		return t
	}
	switch argv[0] {
	case "set":
		t[keywordMode], _ = setTurns(argv[1:], static[1:])
	case "shopt":
		t = shoptTurns(argv[1:], static[1:])
	}
	return t
}

// setTurns reads the words of set as bash 5.3 does for keyword: options
// up to "-", "--" or the first word that starts with neither - nor +, an o
// in them taking the next word for the name of an option unless that is
// empty or starts with one of them. A word made at run time among them may
// be any option (computed): it may turn keyword on. Turned on and off in
// one set, keyword is on: set stops at an option it does not know, with
// the options before it set.
func setTurns(args []string, static []bool) (t turn, computed bool) {
	on, off := false, false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !static[i] {
			return turnsOn, true
		}
		if a == "-" || a == "--" || a == "" || a[0] != '-' && a[0] != '+' {
			break
		}
		for _, c := range a[1:] {
			name := ""
			switch {
			case c == 'k':
				name = "keyword"
			case c != 'o' || i+1 == len(args):
			case !static[i+1]:
				return turnsOn, true
			case args[i+1] != "" && args[i+1][0] != '-' && args[i+1][0] != '+':
				i++
				name = args[i]
			}
			if name == "keyword" {
				on = on || a[0] == '-'
				off = off || a[0] == '+'
			}
		}
	}
	switch {
	case on:
		return turnsOn, false
	case off:
		return turnsOff, false
	}
	return keeps, false
}

// shoptTurns reads the words of shopt as bash does: its options -s, -u,
// -o (with which the names are those of set -o), -p and -q up to the first
// word that is none, then the names of options. A word made at run time
// among the options may be any of them and names, among the names of -s
// any name: it may turn either mode on.
func shoptTurns(args []string, static []bool) (t [numModes]turn) {
	set, unset, o := false, false, false
	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		if !static[i] {
			return [numModes]turn{turnsOn, turnsOn}
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
	m, name := cdableMode, "cdable_vars"
	if o {
		m, name = keywordMode, "keyword"
	}
	for j := i; j < len(args); j++ {
		switch {
		case !static[j] && set, args[j] == name && set:
			t[m] = turnsOn
		case static[j] && args[j] == name:
			t[m] = turnsOff
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

// modeOpts are the options of the modes, by their index in parser.modes,
// with the variable a bash takes each from when it starts: those of set -o
// from SHELLOPTS, those of shopt from BASHOPTS, lists of names split at
// colons. bash keeps both readonly, and exported they hold the options it
// has on.
var modeOpts = [numModes]struct{ name, from string }{
	keywordMode: {"keyword", "SHELLOPTS"},
	cdableMode:  {"cdable_vars", "BASHOPTS"},
}

// optionVars are the variables of modeOpts. bash refuses an assignment to
// them, but env, sudo and a shell other than bash pass them on all the
// same: env SHELLOPTS=keyword bash -c 'git fetch GIT_SSH_COMMAND="sudo ls"'
// runs sudo. A value made at run time may start a shell in any mode.
var optionVars = map[string]bool{"SHELLOPTS": true, "BASHOPTS": true}

// shellModes are the modes of a shell whose options on are opts, by name,
// or that started with env: a bash takes its options from SHELLOPTS and
// BASHOPTS there, and so do those it starts once they are exported.
func shellModes(env, opts []string) (modes [numModes]bool) {
	for m, o := range modeOpts {
		modes[m] = slices.Contains(opts, o.name) || slices.Contains(strings.Split(getenv(env, o.from), ":"), o.name)
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
	for m, o := range modeOpts {
		if o.from == name && slices.Contains(strings.Split(value, ":"), o.name) {
			p.modes[m].ever = true
		}
	}
	return true
}

// started looks at the words args after the name of a shell, its options
// as bash reads them up to its first operand: -k, -o keyword and -O
// cdable_vars start it in a mode, and the code it is handed runs in it, as
// in one the line turns on itself. A word made at run time among them may
// be any option.
func (p *parser) started(args []string, static []bool) {
	on := func(m int) { p.modes[m].ever = true }
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
				if r == 'k' && a[0] == '-' {
					on(keywordMode)
				}
				if r != 'o' && r != 'O' || i+1 == len(args) {
					continue
				}
				// -o takes a name of set -o, -O one of shopt.
				i++
				for m, o := range modeOpts {
					if a[0] == '-' && (r == 'o') == (o.from == "SHELLOPTS") && (!static[i] || args[i] == o.name) {
						on(m)
					}
				}
			}
		default:
			return
		}
	}
}
