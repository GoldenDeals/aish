package policy

import (
	"path/filepath"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// runners are the commands that run code of theirs which their words do
// not show as a command: the window of a multiplexer and the keys it types
// at a shell (tmux.go, screen.go), the template of parallel (parallel.go),
// an alias of git (git.go). What finds the code among the words of such a
// command names the words the code hangs on: one of them made at run time
// marks the code computed instead. The other words made at run time, those
// of git commit -m "$msg" among them, mark nothing.
var runners = map[string]func(w words) []run{
	"env_parallel": func(w words) []run { return parallelRuns(w, false) },
	"git":          gitRuns,
	"parallel":     func(w words) []run { return parallelRuns(w, false) },
	"screen":       screenRuns,
	"sem":          func(w words) []run { return parallelRuns(w, true) },
	"tmux":         tmuxRuns,
}

// words are the words of a command after its name: their text, which for a
// word made at run time is its source, whether each is static and whether
// the shell may make other words of it.
type words struct {
	args          []string
	static, split []bool
}

// run is code a runner hands to a shell, or what it tells of code it keeps:
// text is a line of bash, words the indexes of the words it hangs on, mark a
// kind of Script.Dynamic it adds (rebind for code kept to run later, source,
// stdin, computed for code the line does not hold). stdin is a shell reading
// its commands from the stdin of the runner; moved tells that the code runs
// in another directory, as moves tells of a command; remote that it runs on
// another machine (ssh -o RemoteCommand=, rsync --rsync-path=). env is a
// NAME=VALUE the runner puts in the environment of the code it runs, read
// as an assignment is (tmux set-environment, new-window -e); envDyn tells
// that its value is made at run time. lines finds the code in what a
// runner that reads stdin and is no shell reads there (see sftpLines).
type run struct {
	text   string
	words  []int
	mark   string
	stdin  bool
	moved  bool
	remote bool
	env    string
	envDyn bool
	lines  func(text string) (code []string, mark string)
}

// elsewhere marks runs as code that runs in another directory: in a pane of
// tmux, a window of a session of screen, the top of the repository of git.
func elsewhere(rs []run) []run {
	for i := range rs {
		rs[i].moved = true
	}
	return rs
}

// runs returns the code the runners among the first local words of argv
// hand to a shell, wherever they are among them, as shellC finds shells:
// here that which runs on this machine, there on another. A runner gets all
// the words after it: ssh, the program, reads its options among those of
// its command, which run there. Code that hangs on a word made at run time
// is marked instead. A runner that is the program and reads commands from
// stdin gets them from the redirections of its statement; one of
// optRunners is told whether it is the program.
func (p *parser) runs(argv []string, static, split []bool, local int, redirs []*syntax.Redirect) (here, there []string) {
	for i, a := range argv[:local] {
		name := filepath.Base(a)
		w := words{argv[i+1:], static[i+1:], split[i+1:]}
		var rs []run
		switch {
		case runners[name] != nil:
			rs = runners[name](w)
		case optRunners[name] != nil:
			rs = optRunners[name](w, i == 0)
		default:
			continue
		}
		for _, r := range rs {
			if slices.ContainsFunc(r.words, func(k int) bool { return !w.static[k] }) {
				p.mark(dynComputed)
				continue
			}
			if r.mark != "" {
				p.mark(r.mark)
			}
			if r.env != "" {
				p.setenv(r.env, !r.envDyn)
			}
			if r.moved && !p.remote && (r.stdin || r.text != "") {
				p.chdir = true
			}
			switch {
			case r.stdin && i == 0:
				here = append(here, p.fed(r, p.stdin(redirs))...)
			case r.text == "":
			case r.remote:
				there = append(there, r.text)
			default:
				here = append(here, r.text)
			}
		}
	}
	return here, there
}

// loose returns the indexes of the words before end that the shell may make
// other words or other options of, which moves what the command reads among
// them: a word that splits, or one made at run time that held does not take
// for the value of an option.
func (w words) loose(end int, held func(i int) bool) []int {
	var out []int
	for i := 0; i < min(end, len(w.args)); i++ {
		if w.split[i] || !w.static[i] && !held(i) {
			out = append(out, i)
		}
	}
	return out
}

// held tells, of a command reading its options as g does and having read
// opts, whether a word is the value of an option: whole, or after the fixed
// text of its name, as wrapper.fixed tells.
func held(g getopt, opts []option, args []string) func(int) bool {
	return func(i int) bool { return valued(opts, args, i) || wrapper{opts: g}.fixed(args[i]) }
}

// argv returns the words idx of w as a line of bash that runs them as they
// are, as execvp does: a static word in quotes, one made at run time as a
// word made at run time. A word that may split hangs the line on it.
func (w words) argv(idx []int) run {
	var b strings.Builder
	var deps []int
	for n, k := range idx {
		if n > 0 {
			b.WriteByte(' ')
		}
		if w.static[k] {
			b.WriteString(quoted(w.args[k]))
			continue
		}
		if w.split[k] {
			deps = append(deps, k)
		}
		b.WriteString(unknown(w.args[k]))
	}
	return run{text: b.String(), words: deps}
}

// quoted is s as one word of bash that stays as it is.
func quoted(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// unknown is s as one word of bash made at run time: in $'…', which the
// parser takes for no static word and whose text is s.
func unknown(s string) string {
	return "$'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}

// typed returns the lines a shell reads of text typed at it: a carriage
// return ends a line as a newline does, and so does ^C, which drops it. ok
// is false for any other control character: a tab completes, ^H, ^U, ^W, an
// escape and the like edit the line, which is then not the text.
func typed(text string) (lines string, ok bool) {
	var b strings.Builder
	ok = true
	for _, r := range text {
		switch {
		case r == '\r', r == '\n', r == 3:
			b.WriteByte('\n')
		case r < ' ', r == 0x7f:
			ok = false
			b.WriteByte('\n')
		default:
			b.WriteRune(r)
		}
	}
	return b.String(), ok
}

// sub is a part of the words of a command, and the index among those of
// each of its words.
type sub struct {
	w   words
	idx []int
}

// from returns the words of s from its word i on.
func (s sub) from(i int) sub {
	i = min(i, len(s.idx))
	return sub{words{s.w.args[i:], s.w.static[i:], s.w.split[i:]}, s.idx[i:]}
}

// at maps the words of runs found among the words of s to those of the
// command s is a part of.
func (s sub) at(rs []run) []run {
	for i := range rs {
		ws := make([]int, len(rs[i].words))
		for k, x := range rs[i].words {
			ws[k] = s.idx[x]
		}
		rs[i].words = ws
	}
	return rs
}

// whole is all the words of w as a sub.
func whole(w words) sub {
	return sub{w, indexes(0, len(w.args))}
}
