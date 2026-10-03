package policy

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/inebotov/aish/internal/config"
)

// TrustReason is why the agent may not trust a project file itself; aish
// trust refuses in the same words when the policies were got around.
const TrustReason = "trusting a project is for the user (aish trust)"

// guardChecker keeps the agent from trusting a project file, which would
// let the repository's hooks and tools run: the note "… not trusted; aish
// trust" is in what the model reads too, and a cloned repository may ask
// it to. Load puts it in every engine, whatever the policies and the rules
// say, and a permit does not lift a deny. It judges what a call names, so
// code it cannot see into (python -c, a script, a redirection) is left to
// aish trust's own refusal.
type guardChecker struct{}

func (guardChecker) Check(_ context.Context, in Input) (Decision, error) {
	file := resolve(config.TrustFile())
	// The directory too: moved away and back, it comes with another file.
	names := func(p string) bool { return p == file || p == filepath.Dir(file) }
	deny := Decision{Action: Deny, Reason: TrustReason}
	switch {
	case in.Line != "":
		for _, argv := range in.Commands {
			if trusts(argv) || slices.ContainsFunc(Analyze(argv, in.Cwd, in.Home).Paths, names) {
				return deny, nil
			}
		}
	case in.Tool == "write_file", in.Tool == "edit_file":
		if names(in.Path) {
			return deny, nil
		}
	}
	return Decision{Action: Allow}, nil
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
