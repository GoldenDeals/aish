package policy

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/GoldenDeals/aish/internal/config"
)

// TrustReason is why the agent may not trust a project file itself; aish
// trust refuses in the same words when the policies were got around.
const TrustReason = "trusting a project is for the user (aish trust)"

// guardChecker keeps the agent from trusting a project file, which would
// let the repository's hooks and tools run: the note "… not trusted; aish
// trust" is in what the model reads too, and a cloned repository may ask
// it to. Load puts it in every engine, whatever the policies and the rules
// say, and a permit does not lift a deny. It judges what a call names: the
// operands and redirections of its commands, and a copy or an archive
// unpacked into a directory above trusted.json. A line it cannot see through
// (a parse error, code built at run time, a word only the shell expands)
// is denied when it names trusted.json or aish trust anywhere in its text.
// Code that names neither (python -c, a script, a variable an earlier call
// set) goes unseen: aish trust refuses the agent itself, trusted.json has
// no such keeper.
type guardChecker struct{}

func (guardChecker) Check(_ context.Context, in Input) (Decision, error) {
	g := guardedPaths(in.Home)
	deny := Decision{Action: Deny, Reason: TrustReason}
	switch {
	case in.Line != "":
		if g.line(in) {
			return deny, nil
		}
	case in.Tool == "write_file", in.Tool == "edit_file":
		if g.names(in.Path) {
			return deny, nil
		}
	}
	return Decision{Action: Allow}, nil
}

// Guard is the verdict of the guard alone: what the agent's calls go by
// while the user has the policies off (aish yolo). No policy, rule or
// answer of the user lifts it, so every engine has it, a nil one too.
func (e *Engine) Guard(ctx context.Context, in Input) (Decision, error) {
	if e != nil && e.agent != "" {
		in.Agent = e.agent
	}
	return guardChecker{}.Check(ctx, in)
}

// guarded is where the trust file lives, resolved and as spelled.
type guarded struct {
	// file is trusted.json and its directory: moved away and back, the
	// directory comes with another file.
	file []string
	// above are the directories over that one, up to home and not
	// including it: a tree copied or unpacked into one of them brings a
	// trusted.json of its own.
	above []string
}

func guardedPaths(home string) guarded {
	spelled := filepath.Clean(config.TrustFile())
	var g guarded
	for _, f := range []string{resolve(spelled), spelled} {
		g.file = append(g.file, f, filepath.Dir(f))
		for d := filepath.Dir(filepath.Dir(f)); d != "/" && d != "."; d = filepath.Dir(d) {
			r := resolve(d)
			if under(home, r) {
				break
			}
			g.above = append(g.above, r)
		}
	}
	return g
}

// names tells whether path p of a command may be the trust file or its
// directory when the shell runs it.
func (g guarded) names(p string) bool {
	return slices.ContainsFunc(g.file, func(t string) bool { return reaches(p, t, true) })
}

// over tells whether path p may be a directory above the trust file's.
func (g guarded) over(p string) bool {
	return slices.ContainsFunc(g.above, func(t string) bool { return reaches(p, t, false) })
}

// reaches tells whether path p, which may hold globs and expansions as
// spelled, may be target: it is, or a glob of it matches. With beside an
// expansion that starts a name in target's directory may make it target
// too (~/.local/share/aish/$x); not for the directories above, where it
// would take ~/$x for ~/.local.
func reaches(p, target string, beside bool) bool {
	if p == target {
		return true
	}
	i := strings.IndexAny(p, "$`*?[{")
	if i < 0 {
		return false
	}
	if ok, _ := filepath.Match(p, target); ok {
		return true
	}
	return beside && filepath.Dir(p[:i+1]) == filepath.Dir(target)
}

// line tells whether the commands of a line may trust a project or write
// where the trust file is.
func (g guarded) line(in Input) bool {
	if slices.ContainsFunc(in.Writes, g.names) {
		return true
	}
	enters, copying := false, false
	for i, argv := range in.Commands {
		if len(argv) == 0 {
			continue
		}
		if trusts(argv) {
			return true
		}
		// From the directory of the call and those a cd in the line takes
		// the command to, with what its globs match.
		paths := in.command(i).Paths
		inDir := false
		for _, sh := range in.within(i) {
			paths = append(paths, guardPaths(argv, sh)...)
			d := resolve(sh.pwd)
			inDir = inDir || g.names(d) || g.over(d)
		}
		if slices.ContainsFunc(paths, g.names) {
			return true
		}
		over := slices.ContainsFunc(paths, g.over)
		copier, here := copies(argv)
		if copier && (over || here && (inDir || g.names(in.Cwd) || g.over(in.Cwd))) {
			return true
		}
		copying = copying || copier
		// cd, env -C and the like run the copier after them, or their
		// own, from there, its . and bare names too.
		enters = enters || over && moves(argv)
	}
	return enters && copying || blind(in) && mentions(in.Line)
}

// guardPaths are the files argv names, as the guard looks at them: the
// Paths of Analyze, the value of a name=value or --opt=value word (dd
// of=FILE) and a word Analyze takes for no path at all, as a name in cwd:
// trusted.json is the file when cwd is the trust file's directory.
func guardPaths(argv []string, sh shell) []string {
	c := sh.analyze(argv)
	ps := c.Paths
	for _, w := range c.Args {
		if _, v, ok := strings.Cut(w, "="); ok {
			for _, p := range sh.paths(v) {
				ps = append(ps, walk(p))
			}
		}
	}
	if sh.pwd == "" {
		return ps
	}
	for _, op := range c.Operands {
		if len(sh.paths(op)) == 0 && op != "" && op != "-" {
			ps = append(ps, walk(sh.pwd+"/"+op))
		}
	}
	return ps
}

// copiers put files where an operand says, and with -r a whole tree.
var copiers = map[string]bool{"cp": true, "mv": true, "rsync": true, "install": true}

// execs are the options of find that run the command after them.
var execs = map[string]bool{"-exec": true, "-execdir": true, "-ok": true, "-okdir": true}

// copies tells whether argv runs a copier, or tar extracting, and whether
// it writes in cwd when no operand says where, as tar -x does. cp, mv,
// rsync and tar count anywhere in argv, as trusts looks for aish trust:
// find -exec, env -C DIR, docker cp. install counts as the program or
// after find -exec only: make install PREFIX=~/.local, pip install and
// the like copy nothing the line names.
func copies(argv []string) (copier, here bool) {
	for i, w := range argv {
		switch name := filepath.Base(w); {
		case name == "install" && i > 0 && !execs[argv[i-1]]:
		case copiers[name]:
			return true, false
		case name == "tar" && extracts(argv[i+1:]):
			return true, true
		}
	}
	return false, false
}

// extracts tells whether the arguments of tar make it extract: x in a
// cluster of short options or in the first word of the old form (tar xf),
// or --extract or --get by any prefix getopt takes. -fx.tar is taken for
// x too, on the safe side.
func extracts(args []string) bool {
	for i, a := range args {
		switch {
		case a == "--":
			return false
		case strings.HasPrefix(a, "--"):
			name, _, _ := strings.Cut(a[2:], "=")
			if len(name) > 1 && (strings.HasPrefix("extract", name) || strings.HasPrefix("get", name)) {
				return true
			}
		case strings.HasPrefix(a, "-"), i == 0:
			if strings.Contains(a, "x") {
				return true
			}
		}
	}
	return false
}

// blind tells whether a line may do what the guard does not see: it did
// not parse (bash runs a line up to its error), it runs code built at run
// time (Dynamic: a file written by > "$f" among them), or a word of it is
// an expansion other than of $HOME, $PWD, ~ and ~user ($OLDPWD in ~- among
// them), or braces.
func blind(in Input) bool {
	if in.ParseError != "" || len(in.Dynamic) > 0 {
		return true
	}
	sh := in.shell()
	for _, argv := range in.Commands {
		for _, w := range argv {
			if words, ok := sh.expand(w); !ok || strings.ContainsAny(words[0], "$`") || braces(w) {
				return true
			}
		}
	}
	return false
}

// braces tells whether w has a brace expansion: a comma or .. between
// braces. find's {} is none.
func braces(w string) bool {
	for s := w; ; {
		_, after, ok := strings.Cut(s, "{")
		if !ok {
			return false
		}
		if inner, _, ok := strings.Cut(after, "}"); ok && (strings.Contains(inner, ",") || strings.Contains(inner, "..")) {
			return true
		}
		s = after
	}
}

// mentions tells whether the text of a line names the trust file, or
// trust next to aish: in what the guard cannot see through, that is an
// attempt more often than not.
func mentions(line string) bool {
	l := strings.ToLower(line)
	if strings.Contains(l, strings.ToLower(filepath.Base(config.TrustFile()))) {
		return true
	}
	return strings.Contains(l, "trust") && (strings.Contains(l, "aish") || self() != "" && strings.Contains(l, strings.ToLower(self())))
}

// trusts tells whether argv runs aish trust: as the program, or anywhere
// in it after a program the parser does not unwrap (find -exec). As the
// program aish trusts too with a first operand that may expand to trust,
// and so does a program built at run time with trust for its first
// operand. aish trust --list only reads.
func trusts(argv []string) bool {
	for i, w := range argv {
		rest := argv[i+1:]
		if slices.Equal(rest, []string{"trust", "--list"}) {
			continue
		}
		op := operand(rest)
		switch {
		case isAish(w) && op == "trust":
			return true
		case i == 0 && isAish(w) && !plain(op):
			return true
		case i == 0 && !plain(w) && op == "trust":
			return true
		}
	}
	return false
}

// isAish tells whether a word of a command names aish: by its name, by
// the name the running binary has (the proxy's is the shell's $AISH_BIN),
// or as $AISH_BIN itself.
func isAish(w string) bool {
	switch base := filepath.Base(w); {
	case base == "aish", w == "$AISH_BIN", w == "${AISH_BIN}":
		return true
	case base == self():
		return true
	}
	return false
}

var self = sync.OnceValue(func() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Base(p)
})

// operand is the first word of args that is not an option, "" if none.
func operand(args []string) string {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			return a
		}
	}
	return ""
}

// plain tells whether a word of Commands is the same text when the shell
// runs it: no expansion, substitution, glob or brace in its source form.
// A plain word may be taken for one that is not (a quoted '*'); the other
// way round does not happen.
func plain(w string) bool {
	return !strings.ContainsAny(w, "$`*?[{")
}
