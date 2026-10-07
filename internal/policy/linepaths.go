package policy

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/pattern"
	"mvdan.cc/sh/v3/syntax"
)

// lineVars are the variables the paths of a command are taken by: ~ and
// $HOME are HOME, $PWD and ~+ are PWD, cd - goes to OLDPWD and a relative
// cd looks in CDPATH first. The policy has them from the shell's
// environment, as they are before the line runs: set in the line, they
// make the paths after them other than it takes them.
var lineVars = map[string]bool{"HOME": true, "PWD": true, "OLDPWD": true, "CDPATH": true}

// maxDirs is how many directories the shell may be in at a command of a
// line before the policy gives up following it: a cd through CDPATH, one
// by a link and one that may fail each make more.
const maxDirs = 8

// where is the directories the shell may be in at a point of a line, as
// the shell names them, PWD; lost for a place no policy can know.
type where struct {
	dirs []string
	lost bool
}

var nowhere = where{lost: true}

func (a where) or(b where) where {
	if a.lost || b.lost {
		return nowhere
	}
	out := slices.Clone(a.dirs)
	for _, d := range b.dirs {
		if !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	if len(out) > maxDirs {
		return nowhere
	}
	return where{dirs: out}
}

// site is where a command of a line runs, as far as the line tells, and
// what the shell makes of its words there.
type site struct {
	where
	// nested is code a command of the line hands to a shell, which the
	// tracker does not follow: see settle.
	nested bool
	// spreads holds, by the index of a word in argv, what brace expansion
	// and globbing make of it.
	spreads map[int]spread
}

// spread is what the shell makes of a word by brace expansion and
// globbing: patterns, one per word the braces make, their unquoted text
// as written and their quoted text with what a glob or a prefix would
// take escaped. opaque is one no policy can match: an extended glob, or
// braces making too many words.
type spread struct {
	pats   []string
	opaque bool
}

// lines is what the parser keeps of a line to tell where its commands run.
type lines struct {
	sh shell
	// at is where each command of the line itself runs, as tracked.
	at map[*syntax.CallExpr]where
	// sameShell tells that a command of the line hands code to the shell
	// running it, which runs there and may move it: eval, trap, alias,
	// bind -x, mapfile -C; nestedMove that code handed to a shell changes
	// its directory.
	sameShell, nestedMove bool
}

// shellCode are the commands whose code runs in the shell that runs them,
// as the parser takes it from them.
var shellCode = map[string]bool{"eval": true, "trap": true, "alias": true, "bind": true, "mapfile": true, "readarray": true}

// track follows the directory of the shell through the statements of a
// line as bash runs them: after cd DIR && the commands are in DIR, after
// cd DIR; in DIR or, if cd failed, where they were; a subshell, $(…) and
// & keep their cd to themselves, and so does a pipeline but for its last
// command, which runs in the shell under lastpipe. Where it cannot follow,
// the place is lost: after a cd no policy can know, in a loop that changes
// directory and after it, in the body of a function and after the
// declaration of one that does.
func (l *lines) track(stmts []*syntax.Stmt) {
	l.seq(stmts, where{dirs: []string{l.sh.pwd}})
}

// seq follows a list of statements from w and tells where the shell may be
// when the last of them succeeded and when it failed.
func (l *lines) seq(list []*syntax.Stmt, w where) (ok, fail where) {
	ok, fail = w, w
	for i, s := range list {
		if i > 0 {
			w = ok.or(fail)
		}
		ok, fail = l.stmt(s, w)
	}
	return ok, fail
}

func (l *lines) stmt(s *syntax.Stmt, w where) (ok, fail where) {
	for _, r := range s.Redirs {
		l.subst(r, w)
	}
	if s.Cmd == nil {
		return w, w
	}
	ok, fail = l.cmd(s.Cmd, w)
	switch {
	case s.Background, s.Coprocess, s.Disown:
		return w, w
	case s.Negated:
		return fail, ok
	}
	return ok, fail
}

func (l *lines) cmd(c syntax.Command, w where) (ok, fail where) {
	switch c := c.(type) {
	case *syntax.CallExpr:
		l.subst(c, w)
		l.at[c] = w
		if to, moves := l.cd(c, w); moves {
			return to, w
		}
	case *syntax.BinaryCmd:
		xo, xf := l.stmt(c.X, w)
		switch c.Op {
		case syntax.AndStmt:
			yo, yf := l.stmt(c.Y, xo)
			return yo, xf.or(yf)
		case syntax.OrStmt:
			yo, yf := l.stmt(c.Y, xf)
			return xo.or(yo), yf
		}
		// A pipeline runs its commands in subshells; the last one runs in
		// this shell under shopt lastpipe with job control off.
		yo, yf := l.stmt(c.Y, w)
		return w.or(yo), w.or(yf)
	case *syntax.Subshell:
		l.seq(c.Stmts, w)
	case *syntax.Block:
		return l.seq(c.Stmts, w)
	case *syntax.IfClause:
		co, cf := l.seq(c.Cond, w)
		to, tf := l.seq(c.Then, co)
		out := to.or(tf)
		if c.Else != nil {
			eo, ef := l.cmd(c.Else, cf)
			out = out.or(eo).or(ef)
		} else {
			out = out.or(cf)
		}
		return out, out
	case *syntax.CaseClause:
		l.subst(c.Word, w)
		out := w
		for _, item := range c.Items {
			for _, p := range item.Patterns {
				l.subst(p, w)
			}
			o, f := l.seq(item.Stmts, w)
			out = out.or(o).or(f)
		}
		return out, out
	case *syntax.WhileClause:
		return l.loop(c, w, c.Cond, c.Do)
	case *syntax.ForClause:
		l.subst(c.Loop, w)
		return l.loop(c, w, nil, c.Do)
	case *syntax.FuncDecl:
		// The body runs when the function is called, from wherever the
		// shell is then.
		l.stmt(c.Body, nowhere)
		if movesShell(c.Body) {
			return nowhere, nowhere
		}
	case *syntax.TimeClause:
		if c.Stmt != nil {
			return l.stmt(c.Stmt, w)
		}
	case *syntax.CoprocClause:
		l.stmt(c.Stmt, w)
	default:
		// Arithmetic, tests, declare and let run commands only in their
		// substitutions.
		l.subst(c, w)
	}
	return w, w
}

// loop follows a loop: from w when it moves the shell nowhere, else from
// a lost place, as a turn starts where the one before left the shell.
func (l *lines) loop(n syntax.Node, w where, cond, body []*syntax.Stmt) (ok, fail where) {
	if movesShell(n) {
		w = nowhere
	}
	l.seq(cond, w)
	l.seq(body, w)
	return w, w
}

// subst follows the commands of the substitutions in n, $(…), `…`, <(…)
// and >(…): each runs in a subshell started from w.
func (l *lines) subst(n syntax.Node, w where) {
	syntax.Walk(n, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.CmdSubst:
			l.seq(n.Stmts, w)
			return false
		case *syntax.ProcSubst:
			l.seq(n.Stmts, w)
			return false
		}
		return true
	})
}

// movesShell tells whether code may change the directory of the shell
// running it: it has a cd, pushd or popd, or a command built at run time,
// which may be one, anywhere in it.
func movesShell(n syntax.Node) bool {
	found := false
	syntax.Walk(n, func(n syntax.Node) bool {
		if call, ok := n.(*syntax.CallExpr); ok && !found {
			argv, static := callWords(call)
			argv, static = pastBuiltin(argv, static)
			found = len(argv) > 0 && (!static[0] || chdirsShell(argv[0]))
		}
		return !found
	})
	return found
}

// chdirsShell tells whether name is a builtin changing the directory of
// the shell: /usr/bin/cd runs in a process of its own.
func chdirsShell(name string) bool {
	return name == "cd" || name == "pushd" || name == "popd"
}

func callWords(call *syntax.CallExpr) ([]string, []bool) {
	argv := make([]string, len(call.Args))
	static := make([]bool, len(call.Args))
	for i, w := range call.Args {
		argv[i], static[i] = word(w), isStatic(w)
	}
	return argv, static
}

// pastBuiltin returns argv past the builtin and command words that run the
// builtin after them in this shell; empty when they run none (command -v).
func pastBuiltin(argv []string, static []bool) ([]string, []bool) {
	for len(argv) > 0 && static[0] && (argv[0] == "builtin" || argv[0] == "command") {
		opts, cmd := wrappers[argv[0]].read(argv[1:])
		if has(opts, "v", "V") {
			return nil, nil
		}
		argv, static = argv[1+cmd:], static[1+cmd:]
	}
	return argv, static
}

// cd tells whether call may change the directory of the shell, and where
// it leaves it from w when it succeeds: a cd, pushd or popd, run as itself
// or by builtin or command, or a command built at run time, which may be
// one of them. From a lost place only a cd to an absolute directory is
// known.
func (l *lines) cd(call *syntax.CallExpr, w where) (where, bool) {
	argv, static := pastBuiltin(callWords(call))
	switch {
	case len(argv) == 0:
		return w, false
	case !static[0]:
		return nowhere, true
	case !chdirsShell(argv[0]):
		return w, false
	}
	from := w.dirs
	if w.lost {
		// Not a directory: a path taken from it stays relative.
		from = []string{"."}
	}
	var to where
	for _, d := range from {
		sh := l.sh
		sh.pwd = d
		dirs, ok := sh.enters(argv)
		if !ok || slices.ContainsFunc(dirs, func(d string) bool { return !filepath.IsAbs(d) }) {
			return nowhere, true
		}
		to = to.or(where{dirs: dirs})
	}
	return to, true
}

// enters returns the directories cd, pushd or popd in argv may leave the
// shell in when it succeeds, run from sh.pwd: the path as spelled with its
// .. taken off, the shell's name for it, unless told -P, and the one the
// kernel walks to, where cd goes when the first fails; sh.pwd for what
// moves nothing: pushd -n, popd -n, cd with no HOME, which fails. ok is
// false where it goes no policy can know.
func (sh shell) enters(argv []string) (dirs []string, ok bool) {
	c := sh.analyze(argv)
	if c.lost {
		return nil, false
	}
	if c.Program != "cd" && slices.Contains(c.Flags, "n") {
		return []string{sh.pwd}, true
	}
	ops := c.Operands
	if c.Program == "pushd" {
		ops = slices.DeleteFunc(slices.Clone(ops), stackIndex)
	}
	var places []string
	switch {
	case c.Program == "cd" && len(ops) == 0 && sh.home == "":
		return []string{sh.pwd}, true
	case c.Program == "cd" && len(ops) == 0:
		places = sh.lookup(sh.home)
	case len(ops) > 1:
		// cd a b fails in bash 5.
		dirs = append(dirs, sh.pwd)
	}
	for _, op := range ops {
		words, _ := sh.expand(op)
		for _, w := range words {
			places = append(places, sh.lookup(w)...)
		}
	}
	logical := c.Program == "pushd" || !physicalCd(c.Args)
	for _, p := range places {
		if logical {
			dirs = append(dirs, filepath.Clean(p))
		}
		dirs = append(dirs, walk(p))
	}
	return slices.Compact(dirs), true
}

// placed is call of the parser, which records where each command it adds
// runs: where the tracker found call, and for the command of a wrapper
// that runs it in another directory (env -C, sudo -D, sudo -i, chroot)
// there. chroot and sudo -R take every path of their command from
// another root, which no policy follows: they are marked.
func (p *parser) placed(call *syntax.CallExpr, redirs []*syntax.Redirect) []snippet {
	from := len(p.out)
	code := p.call(call, redirs)
	if p.line == nil {
		return code
	}
	for len(p.sites) < from {
		p.sites = append(p.sites, site{nested: true})
	}
	w, tracked := p.line.at[call]
	chain := p.out[from:]
	spreads := spreadsOf(call, chain)
	for k, argv := range chain {
		p.sites = append(p.sites, site{where: w, nested: !tracked, spreads: spreads[k]})
		if p.remote {
			continue
		}
		name := filepath.Base(argv[0])
		switch {
		case tracked && shellCode[name]:
			p.line.sameShell = true
		case !tracked && (chdirsShell(argv[0]) || !plain(argv[0])):
			p.line.nestedMove = true
		}
		if roots(argv) {
			p.mark(dynComputed)
		}
		if to, ok := p.line.sh.elsewhere(argv, w); ok {
			w = to
		}
	}
	return code
}

// roots tells whether argv runs a command under another root: chroot,
// sudo -R, and the options of rooted.
func roots(argv []string) bool {
	switch name := filepath.Base(argv[0]); {
	case name == "chroot":
		return true
	case name == "sudo":
		opts, _ := wrappers["sudo"].read(argv[1:])
		return has(opts, "R", "chroot")
	case rooted[name] != nil:
		opts, _ := wrappers[name].read(argv[1:])
		return has(opts, rooted[name]...)
	}
	return false
}

// elsewhere tells whether a wrapper in argv runs its command in another
// directory, and which, run from w: the one of env -C and sudo -D when it
// is written out, else a lost one (sudo -i, chroot).
func (sh shell) elsewhere(argv []string, w where) (where, bool) {
	wr, ok := wrappers[filepath.Base(argv[0])]
	if !ok || !moves(argv) {
		return w, false
	}
	opts, _ := wr.read(argv[1:])
	dir := ""
	for _, o := range opts {
		switch {
		case !slices.Contains(wr.chdir, o.name):
		case o.name == "C" || o.name == "D" || o.name == "chdir":
			dir = o.value
		default:
			return nowhere, true
		}
	}
	words, ok := sh.expand(dir)
	if dir == "" || !ok || len(words) != 1 || strings.ContainsAny(words[0], "$`") || expands(dir) {
		return nowhere, true
	}
	if filepath.IsAbs(words[0]) {
		return where{dirs: words}, true
	}
	if w.lost {
		return nowhere, true
	}
	var to where
	for _, d := range w.dirs {
		to = to.or(where{dirs: []string{d + "/" + words[0]}})
	}
	return to, true
}

// settle decides where the commands handed to a shell run, and marks a
// line whose paths no policy can know before it runs. The tracker does
// not follow code handed to a shell: it runs where the line started when
// nothing in the line changes directory, and is lost otherwise. Such code
// that changes directory, in a line that hands code to its own shell (eval
// 'cd /etc'), may move it anywhere among its commands: all of them are
// lost then.
func (p *parser) settle() []site {
	if p.line == nil {
		return nil
	}
	for len(p.sites) < len(p.out) {
		p.sites = append(p.sites, site{nested: true})
	}
	everywhere := p.line.sameShell && p.line.nestedMove
	for i := range p.sites {
		s := &p.sites[i]
		switch {
		case everywhere, s.nested && p.chdir:
			s.where = nowhere
		case s.nested:
			s.where = where{}
		}
	}
	for i, argv := range p.out {
		if slices.Contains(p.remotes, i) {
			continue
		}
		if _, unsure := p.line.sh.at(argv, p.sites[i]); unsure {
			p.mark(dynComputed)
		}
	}
	return p.sites
}

// command is Analyze of the i-th command of Commands where the line runs
// it (see shell.at): Analyze takes every command from the directory of the
// call.
func (in Input) command(i int) Command {
	sh := in.shell()
	if i < len(in.sites) {
		c, _ := sh.at(in.Commands[i], in.sites[i])
		return c
	}
	return sh.analyze(in.Commands[i])
}

// within returns the shells the i-th command of Commands may run in: that
// of the call, and those in the directories the line may have taken it to.
func (in Input) within(i int) []shell {
	sh := in.shell()
	out := []shell{sh}
	if i < len(in.sites) && !in.sites[i].lost {
		for _, d := range in.sites[i].dirs {
			if d != sh.pwd {
				sd := sh
				sd.pwd = d
				out = append(out, sd)
			}
		}
	}
	return out
}

// at is analyze of argv run at site s of its line. Its paths are taken
// from each directory it may run in; where the line took the shell to
// another directory, its bare words name files there too: in the shell's
// own a bare word is a name (main, install), in another the policy has no
// other way to see the file it names. A word the shell globs or expands
// by braces adds the paths it becomes, and the files it matches; the word
// as written stays among the paths, as bash keeps a glob that matches
// nothing. unsure tells that some of the paths no policy can know before
// the line runs: one taken from the current directory where that is lost,
// and a glob that matches nothing in a directory that exists (the line may
// make the file it matches), more than maxMatches files, or what the
// policy cannot match.
func (sh shell) at(argv []string, s site) (c Command, unsure bool) {
	c = sh.analyze(argv)
	if sh.pwd == "" || len(argv) == 0 || !s.lost && len(s.spreads) == 0 && !sh.moved(s.dirs) {
		return c, false
	}
	cd := chdirsShell(c.Program)
	if s.lost {
		// The paths stay those taken from the directory of the call.
		for _, i := range operandIndexes(argv) {
			sp, spreads := s.spreads[i]
			switch op := argv[i]; {
			case cd:
				words, ok := sh.expand(op)
				unsure = unsure || ok && slices.ContainsFunc(words, func(w string) bool { return !filepath.IsAbs(w) })
			case spreads:
				unsure = sh.spread(&c, sp, false, true) || unsure
			default:
				unsure = unsure || sh.fromCwd(op)
			}
		}
		return c, unsure
	}
	dirs := s.dirs
	if len(dirs) == 0 {
		dirs = []string{sh.pwd}
	}
	own := resolve(sh.pwd)
	c.Paths = nil
	for _, d := range dirs {
		sd := sh
		sd.pwd = d
		for _, path := range sd.analyze(argv).Paths {
			c.add(path)
		}
		if cd {
			continue
		}
		away := resolve(d) != own
		for _, i := range operandIndexes(argv) {
			if sp, ok := s.spreads[i]; ok {
				unsure = sd.spread(&c, sp, away, false) || unsure
			} else if away {
				sd.bare(&c, argv[i])
			}
		}
	}
	return c, unsure
}

// moved tells whether dirs are other than the shell's own directory.
func (sh shell) moved(dirs []string) bool {
	return len(dirs) > 1 || len(dirs) == 1 && dirs[0] != sh.pwd
}

// operandIndexes are the indexes in argv of the operands as analyze takes
// them.
func operandIndexes(argv []string) []int {
	var out []int
	end := false
	for i, a := range argv[1:] {
		switch {
		case end || a == "-" || !strings.HasPrefix(a, "-"):
			out = append(out, 1+i)
		case a == "--":
			end = true
		}
	}
	return out
}

// fromCwd tells whether op names a path taken from the current directory:
// ./x, a/b, .., $PWD/x, ~+/x.
func (sh shell) fromCwd(op string) bool {
	for _, p := range []string{"~+", "$PWD", "${PWD}"} {
		if strings.HasPrefix(op, p) {
			return true
		}
	}
	words, ok := sh.expand(op)
	return ok && slices.ContainsFunc(words, func(w string) bool {
		return !filepath.IsAbs(w) && (w == "." || w == ".." || strings.Contains(w, "/"))
	})
}

// bare adds to c the file a bare word names in sh.pwd.
func (sh shell) bare(c *Command, op string) {
	words, ok := sh.expand(op)
	if !ok || op == "-" {
		return
	}
	for _, w := range words {
		if w != "" && bareName(w) && !filepath.IsAbs(w) {
			c.add(walk(sh.pwd + "/" + w))
		}
	}
}

func bareName(w string) bool {
	return w != "." && w != ".." && !strings.Contains(w, "/")
}

// spread adds to c the paths of a word the shell expands by braces and
// globs, as at does, and tells whether some of them are unknown. A bare
// pattern counts in a directory the line took the shell to (away) only;
// one taken from a lost directory is unknown.
func (sh shell) spread(c *Command, sp spread, away, lost bool) (unsure bool) {
	if sp.opaque {
		return true
	}
	for _, pat := range sp.pats {
		full, rel, ok := sh.pattern(pat)
		switch {
		case !ok:
			unsure = true
			continue
		case rel && bareName(pat) && !away:
			continue
		case rel && lost:
			unsure = true
			continue
		}
		if !pattern.HasMeta(full, 0) {
			c.add(walk(unescape(full)))
			continue
		}
		ms, known := matchFiles(full)
		for _, m := range ms {
			c.add(walk(m))
		}
		if !known {
			unsure = true
		}
	}
	return unsure
}

// pattern makes pat absolute: a leading ~, ~user, ~+, $HOME or $PWD the
// directory it stands for, and one without from sh.pwd (rel, as for ~+ and
// $PWD). ok is false for a prefix whose directory is not known (~-, no
// HOME) or an expansion the shell's prefix does not take ($HOME*).
func (sh shell) pattern(pat string) (full string, rel, ok bool) {
	dir, rest, found := sh.prefix(pat)
	switch {
	case found && dir == "":
		return "", false, false
	case found:
		rel = strings.HasPrefix(pat, "~+") || strings.HasPrefix(pat, "$PWD") || strings.HasPrefix(pat, "${PWD}")
		return pattern.QuoteMeta(dir, 0) + rest, rel, true
	case strings.HasPrefix(pat, "$"):
		return "", false, false
	case strings.HasPrefix(pat, "/"):
		return pat, false, true
	}
	return pattern.QuoteMeta(sh.pwd, 0) + "/" + pat, true, true
}

// maxMatches is how many files a glob may match, and how many words braces
// may make, before the policy gives up on them.
const maxMatches = 1024

// matchFiles returns the files an absolute pattern matches. It matches
// names starting with a dot and in any case, as bash does with dotglob or
// nocaseglob, and . and .. for a pattern starting with a dot, as bash
// before 5.2 does: a superset of what the shell may take. known is false
// for a pattern that may match files the list does not hold: one matching
// nothing in a directory that is there, a component ** (globstar), a bad
// pattern, more than maxMatches files. A glob below a directory that is
// not there matches no file of it, as the other paths to it.
func matchFiles(pat string) (matches []string, known bool) {
	comps := strings.Split(pat, "/")
	cur := []string{""}
	first := true
	for _, comp := range comps[1:] {
		if !pattern.HasMeta(comp, 0) {
			for i := range cur {
				cur[i] += "/" + unescape(comp)
			}
			continue
		}
		if comp == "**" {
			return nil, false
		}
		expr, err := pattern.Regexp(comp, pattern.EntireString|pattern.NoGlobCase)
		if err != nil {
			return nil, false
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			return nil, false
		}
		var next []string
		for _, dir := range cur {
			entries, err := os.ReadDir(dir + "/")
			switch {
			case err == nil:
			case first && (errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)):
				return nil, true
			case first:
				return nil, false
			default:
				// A match of the component before that is no directory.
				continue
			}
			names := make([]string, 0, len(entries)+2)
			for _, e := range entries {
				names = append(names, e.Name())
			}
			if strings.HasPrefix(comp, ".") {
				names = append(names, ".", "..")
			}
			for _, n := range names {
				if re.MatchString(n) {
					next = append(next, dir+"/"+n)
				}
				if len(next) > maxMatches {
					return next, false
				}
			}
		}
		if first && len(next) == 0 {
			return nil, false
		}
		first = false
		cur = next
	}
	for _, m := range cur {
		if _, err := os.Lstat(m); err == nil {
			matches = append(matches, m)
		}
	}
	return matches, len(matches) > 0
}

// spreadsOf is what the shell makes of the words of call by brace
// expansion and globbing, for each argv of chain, the commands call runs
// one inside the other, by the index of the word in it: the words of a
// wrapper's command are the last ones of its own, but for those of an env
// -S string, which env does not glob.
func spreadsOf(call *syntax.CallExpr, chain [][]string) []map[int]spread {
	out := make([]map[int]spread, len(chain))
	var all map[int]spread
	for k, w := range call.Args {
		if sp, ok := spreadOf(w); ok {
			if all == nil {
				all = map[int]spread{}
			}
			all[k] = sp
		}
	}
	if all == nil {
		return out
	}
	n := len(call.Args)
	for c, argv := range chain {
		for j := 1; j <= len(argv) && j <= n; j++ {
			if argv[len(argv)-j] != word(call.Args[n-j]) {
				break
			}
			if sp, ok := all[n-j]; ok {
				if out[c] == nil {
					out[c] = map[int]spread{}
				}
				out[c][len(argv)-j] = sp
			}
		}
	}
	return out
}

// spreadOf is what the shell makes of w by brace expansion and globbing;
// false when it makes w alone, and for a word of other expansions, as
// "$d"/*: a policy cannot know it either way, and has it as spelled.
func spreadOf(w *syntax.Word) (spread, bool) {
	globbed := false
	for _, part := range w.Parts {
		switch part := part.(type) {
		case *syntax.Lit:
			globbed = globbed || expands(part.Value)
		case *syntax.ExtGlob:
			return spread{opaque: true}, true
		}
	}
	if !globbed {
		return spread{}, false
	}
	b := &syntax.Word{Parts: slices.Clone(w.Parts)}
	syntax.SplitBraces(b)
	var sp spread
	for x, err := range expand.BracesSeq(nil, b) {
		if err != nil || len(sp.pats) == maxMatches {
			return spread{opaque: true}, true
		}
		pat, ok := patternOf(x)
		if !ok {
			return spread{}, false
		}
		sp.pats = append(sp.pats, pat)
	}
	return sp, true
}

// patternOf is the pattern of a word past brace expansion: its unquoted
// text as written, backslashes and all, and its quoted text escaped; a
// leading $HOME or $PWD stays for the shell's prefix. false for a word
// made of other expansions.
func patternOf(w *syntax.Word) (string, bool) {
	var b strings.Builder
	for i, part := range w.Parts {
		switch part := part.(type) {
		case *syntax.Lit:
			b.WriteString(part.Value)
		case *syntax.SglQuoted:
			if part.Dollar {
				return "", false
			}
			b.WriteString(quotedPattern(part.Value))
		case *syntax.DblQuoted:
			if part.Dollar {
				return "", false
			}
			for j, q := range part.Parts {
				switch q := q.(type) {
				case *syntax.Lit:
					b.WriteString(quotedPattern(unescapeOnly(q.Value, "$`\"\\")))
				case *syntax.ParamExp:
					if i > 0 || j > 0 || !dirParam(q) {
						return "", false
					}
					b.WriteString(printParam(q))
				default:
					return "", false
				}
			}
		case *syntax.ParamExp:
			if i > 0 || !dirParam(part) {
				return "", false
			}
			b.WriteString(printParam(part))
		default:
			return "", false
		}
	}
	return b.String(), true
}

// quotedPattern escapes text the shell took in quotes for a pattern: its
// glob characters, and ~ and $, which would make a prefix of it.
func quotedPattern(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`*?[]\~$`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// dirParam tells whether pe is $HOME, ${HOME}, $PWD or ${PWD}.
func dirParam(pe *syntax.ParamExp) bool {
	switch printParam(pe) {
	case "$HOME", "${HOME}", "$PWD", "${PWD}":
		return true
	}
	return false
}

func printParam(pe *syntax.ParamExp) string {
	var b strings.Builder
	if err := syntax.NewPrinter().Print(&b, pe); err != nil {
		return ""
	}
	return b.String()
}
