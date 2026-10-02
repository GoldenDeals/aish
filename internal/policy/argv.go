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
	Paths    []string // operands that look like paths, absolute, cleaned and with symlinks resolved
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
	for _, op := range c.Operands {
		if p, ok := pathOf(op, cwd, home); ok {
			c.Paths = append(c.Paths, resolve(filepath.Clean(p)))
		}
	}
	return c
}

// pathOf tells whether an operand names a path and makes it absolute. Bare
// words (install, main) are not paths; words with other expansions than
// $HOME cannot be resolved and are not paths either.
func pathOf(op, cwd, home string) (string, bool) {
	for _, pfx := range []string{"~", "$HOME", "${HOME}"} {
		if rest, ok := strings.CutPrefix(op, pfx); ok && (rest == "" || strings.HasPrefix(rest, "/")) {
			return home + rest, true
		}
	}
	switch {
	case strings.ContainsAny(op, "$`"):
		return "", false
	case filepath.IsAbs(op):
		return op, true
	case op == "." || op == ".." || strings.Contains(op, "/"):
		return filepath.Join(cwd, op), true
	}
	return "", false
}
