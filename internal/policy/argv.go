package policy

import (
	"errors"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
)

// Command is what a policy learns about one simple command. The facts are
// computed here once, so that `rm -rf ~/`, `rm -rf "$HOME"` and
// `rm -rf /home/me/../me/` need a single rule on Paths instead of a list of
// spellings in every policy file.
type Command struct {
	Program  string   // basename of argv[0]
	Args     []string // argv[1:] as written
	Flags    []string // "-rf" → "r", "f"; "--force" → "force"; "--opt=v" → "opt"; none after "--"
	Operands []string // non-option args, "--" dropped
	Paths    []string // operands that look like paths, absolute and resolved as the kernel opens them; for cd and pushd every place they may enter (see shell.chdir)
	Text     string   // argv joined with spaces
	// lost tells that cd, pushd or popd may take the shell to a directory
	// no policy can know before the line runs: $OLDPWD, one of the
	// directory stack, a name the shell builds.
	lost bool
}

// shell is what the paths of a command depend on in the shell running it.
type shell struct {
	// pwd is the logical directory: the shell names it by the symlinks it
	// was entered through, and `cd ..` goes up from there.
	pwd string
	// home is HOME, "" when it is unset: ~ and $HOME are unknown then.
	home string
	// cdpath is CDPATH, the directories cd looks a relative name up in
	// before pwd.
	cdpath string
	// quoted tells that the line may hold a ~ or $ at the start of a word
	// that quotes keep from expanding (see quotedPrefix).
	quoted bool
	// env tells that the fields above are the shell's, from NewInput: an
	// Input made otherwise is in Cwd, with Home for HOME.
	env bool
}

// Analyze derives the facts of argv as run from cwd by a user whose home
// is home, in a shell without CDPATH. cwd must be absolute; cd goes up
// from it as it is spelled, through the links in it.
func Analyze(argv []string, cwd, home string) Command {
	return shell{pwd: cwd, home: home}.analyze(argv)
}

func (sh shell) analyze(argv []string) Command {
	c := Command{Text: strings.Join(argv, " ")}
	if len(argv) == 0 {
		return c
	}
	if argv[0] != "" {
		c.Program = filepath.Base(argv[0])
	}
	c.Args = argv[1:]
	end := false
	for _, a := range c.Args {
		switch {
		case end || a == "-" || !strings.HasPrefix(a, "-"):
			c.Operands = append(c.Operands, a)
		case a == "--":
			end = true
		case strings.HasPrefix(a, "--"):
			name, _, _ := strings.Cut(a[2:], "=")
			c.Flags = append(c.Flags, name)
		default:
			for _, r := range a[1:] {
				c.Flags = append(c.Flags, string(r))
			}
		}
	}
	switch c.Program {
	case "cd", "pushd", "popd":
		sh.chdir(&c)
		return c
	}
	for _, op := range c.Operands {
		for _, p := range sh.paths(op) {
			c.add(walk(p))
		}
	}
	return c
}

// paths returns the paths an operand of a command other than cd may name,
// absolute and as spelled. Bare words (install, main) are not paths, and
// neither is a word no policy can know (see expand).
func (sh shell) paths(op string) []string {
	words, ok := sh.expand(op)
	if !ok {
		return nil
	}
	var ps []string
	for _, w := range words {
		switch {
		case filepath.IsAbs(w):
		case w == "." || w == ".." || strings.Contains(w, "/"):
			// Not filepath.Join: it would take link/.. away before the
			// link is followed.
			w = sh.pwd + "/" + w
		default:
			continue
		}
		ps = append(ps, w)
	}
	return ps
}

func (c *Command) add(path string) {
	if !slices.Contains(c.Paths, path) {
		c.Paths = append(c.Paths, path)
	}
}

// chdir finds where cd, pushd or popd may take the shell. An operand of cd
// or pushd is a directory however it is spelled, and cd without one goes
// home; a relative one is looked up in CDPATH first. cd takes ".." off the
// path as spelled before it follows the links, and enters the path as the
// kernel walks it only when that fails: unless told -P, both places are in
// Paths. Where it goes by $OLDPWD (-), the directory stack (popd, pushd
// alone, +N, -N) or a name the shell builds, c is lost; pushd -n and
// popd -n change the stack alone.
func (sh shell) chdir(c *Command) {
	nocd := c.Program != "cd" && slices.Contains(c.Flags, "n")
	dirs := c.Operands
	switch c.Program {
	case "popd":
		c.lost = !nocd
		return
	case "pushd":
		dirs = slices.DeleteFunc(slices.Clone(dirs), stackIndex)
		if slices.ContainsFunc(c.Args, stackIndex) || len(dirs) == 0 {
			c.lost = !nocd
		}
	}
	logical := c.Program == "pushd" || !physicalCd(c.Args)
	enter := func(dir string) {
		for _, p := range sh.lookup(dir) {
			w := walk(p)
			if l := resolve(p); logical && l != w {
				c.add(l)
			}
			c.add(w)
		}
	}
	if c.Program == "cd" && len(dirs) == 0 {
		// HOME as it is, not expanded again; without one cd fails.
		if sh.home != "" {
			enter(sh.home)
		}
		return
	}
	for _, op := range dirs {
		words, ok := sh.expand(op)
		if !ok || op == "-" || expands(op) || strings.ContainsAny(words[0], "$`") {
			c.lost = true
			continue
		}
		for _, w := range words {
			enter(w)
		}
	}
}

// physicalCd tells whether cd is told -P: the last of -L and -P among the
// options before its operand counts, as in bash. pushd has no such options
// and always goes by the logical path.
func physicalCd(args []string) bool {
	p := false
	for _, a := range args {
		if a == "--" || a == "-" || !strings.HasPrefix(a, "-") {
			break
		}
		for _, r := range a[1:] {
			switch r {
			case 'P':
				p = true
			case 'L':
				p = false
			}
		}
	}
	return p
}

// stackIndex tells whether a word of pushd or popd is +N or -N, a place in
// the directory stack.
func stackIndex(s string) bool {
	return len(s) > 1 && (s[0] == '+' || s[0] == '-') && digits(s[1:])
}

// lookup returns the places cd tries for dir, absolute and as spelled: a
// relative dir in each directory of CDPATH, an empty one being the
// current, and then in pwd. A dir starting with . or .. is not looked up.
func (sh shell) lookup(dir string) []string {
	if filepath.IsAbs(dir) {
		return []string{dir}
	}
	var out []string
	if dir != "." && dir != ".." && !strings.HasPrefix(dir, "./") && !strings.HasPrefix(dir, "../") {
		for _, d := range filepath.SplitList(sh.cdpath) {
			if !filepath.IsAbs(d) {
				d = sh.pwd + "/" + d
			}
			out = append(out, d+"/"+dir)
		}
	}
	return append(out, sh.pwd+"/"+dir)
}

// expand returns the words op may be once the shell has expanded it: a
// leading ~, ~+, ~user, $HOME or $PWD (braced or not) is the directory it
// stands for, and where the line may hold it quoted, the word as written
// is a second choice. What follows the directory is kept as spelled, $ or
// glob included: ~/.local/$x is a name in ~/.local, and the guard tells
// what it may match. ok is false for a word no policy can know: starting
// with ~- or the directory stack, a user the lookup fails for, any other
// $ or a backquote.
func (sh shell) expand(op string) (words []string, ok bool) {
	dir, rest, found := sh.prefix(op)
	switch {
	case !found && strings.ContainsAny(op, "$`"):
		return nil, false
	case !found:
		return []string{op}, true
	case dir == "":
		return nil, false
	case sh.quoted:
		return []string{dir + rest, op}, true
	}
	return []string{dir + rest}, true
}

// prefix splits off the expansion op starts with that names a directory,
// if any: dir is "" for one whose directory cannot be known. The tilde
// prefix runs up to the first slash, as in bash, and ~name of no user is
// left as it is written.
func (sh shell) prefix(op string) (dir, rest string, found bool) {
	if strings.HasPrefix(op, "~") {
		i := strings.IndexByte(op, '/')
		if i < 0 {
			i = len(op)
		}
		name, tail := op[1:i], op[i:]
		switch {
		case name == "":
			return sh.home, tail, true
		case name == "+":
			return sh.pwd, tail, true
		case name == "-", digits(name), stackIndex(name):
			// $OLDPWD and the directory stack.
			return "", tail, true
		}
		u, err := user.Lookup(name)
		var unknown user.UnknownUserError
		switch {
		case err == nil:
			return u.HomeDir, tail, true
		case errors.As(err, &unknown):
			return "", op, false
		}
		return "", tail, true
	}
	for _, p := range []struct{ word, dir string }{
		{"$HOME", sh.home}, {"${HOME}", sh.home},
		{"$PWD", sh.pwd}, {"${PWD}", sh.pwd},
	} {
		if rest, ok := strings.CutPrefix(op, p.word); ok && (rest == "" || strings.HasPrefix(rest, "/")) {
			return p.dir, rest, true
		}
	}
	return "", op, false
}

// quotedPrefix tells whether line may hold a ~ or a $ that quotes or a
// backslash keep from expanding at the start of a word: "~/x", '$HOME',
// \~. Commands hold their words without the quotes, so a word starting
// with ~ or $HOME there may as well be a name as it is written.
func quotedPrefix(line string) bool {
	for _, s := range []string{`"~`, `'~`, `\~`, `'$`, `\$`} {
		if strings.Contains(line, s) {
			return true
		}
	}
	return false
}
