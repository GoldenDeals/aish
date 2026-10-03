package policy

import (
	"path/filepath"
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
	Paths    []string // operands that look like paths, absolute and resolved as the kernel opens them (cd, pushd: also as they take them logically)
	Text     string   // argv joined with spaces
}

// Analyze derives the facts of argv as run from cwd by a user whose home
// is home. Both must be absolute; home must already be resolved.
func Analyze(argv []string, cwd, home string) Command {
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
	logical := c.Program == "pushd" || c.Program == "cd" && !physicalCd(c.Args)
	for _, op := range c.Operands {
		p, ok := pathOf(op, cwd, home)
		if !ok {
			continue
		}
		w := walk(p)
		// cd takes ".." off the path as spelled before it follows the
		// links, and enters the path as the kernel walks it only when that
		// fails: a rule must see both places.
		if l := resolve(p); logical && l != w {
			c.Paths = append(c.Paths, l)
		}
		c.Paths = append(c.Paths, w)
	}
	return c
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

// pathOf tells whether an operand names a path and makes it absolute. Bare
// words (install, main) are not paths; words with other expansions than
// $HOME and $PWD cannot be resolved and are not paths either.
func pathOf(op, cwd, home string) (string, bool) {
	for _, pfx := range []struct{ word, dir string }{
		{"~", home}, {"$HOME", home}, {"${HOME}", home},
		{"$PWD", cwd}, {"${PWD}", cwd},
	} {
		if rest, ok := strings.CutPrefix(op, pfx.word); ok && (rest == "" || strings.HasPrefix(rest, "/")) {
			return pfx.dir + rest, true
		}
	}
	switch {
	case strings.ContainsAny(op, "$`"):
		return "", false
	case filepath.IsAbs(op):
		return op, true
	case op == "." || op == ".." || strings.Contains(op, "/"):
		// Not filepath.Join: it would take link/.. away before the link
		// is followed.
		return cwd + "/" + op, true
	}
	return "", false
}
