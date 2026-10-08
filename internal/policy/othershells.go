package policy

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// The policy reads the code of every shell it knows by name as bash reads
// it: bash, sh and dash with bash's options, the shells of the ksh and ash
// kin with the options they take alike, a zsh as a zsh may read it too
// (see zshes). The language of a shell of another kind — fish, tcsh, nu —
// it does not read: code handed to one is computed, and it is parsed as
// bash all the same, which finds the rm of fish -c 'rm -rf x'. A shell is
// known by the name of its file with a version after it too: ksh93,
// mksh-R59, bash5.2, bash-5.2.37.

// The kinds of shell, by the name of its program (see shellKind).
const (
	notShell = iota
	// bashShell takes the options of bash: bash, sh, dash.
	bashShell
	// zshKind is a zsh (see isZsh).
	zshKind
	// posixShell reads its code as bash does, and takes options bash does
	// not know: one that is none of posixFlags may take the word after it,
	// and the script is then not where bash finds it (mksh -T TTY -c CMD).
	posixShell
	// foreignShell is a shell of another language.
	foreignShell
)

// shellNames are the shells known by name, with their kinds.
var shellNames = map[string]int{"rzsh": zshKind}

func init() {
	for kind, names := range map[int]string{
		bashShell:    "bash dash rbash sh",
		posixShell:   "ash bosh brush hush jsh ksh lksh loksh mksh oksh osh pbosh pdksh posh rksh yash",
		foreignShell: "csh elvish es fish hilbish ion murex nu oil powershell pwsh pwsh-preview rc scsh tcsh xonsh ysh",
	} {
		for _, name := range strings.Fields(names) {
			shellNames[name] = kind
		}
	}
}

// shellVersion is what may follow the name of a shell in the name of its
// file: 93, -R59, 5.2, -5.2.37.
var shellVersion = regexp.MustCompile(`^[-_.]?[vVrR]?[0-9][0-9A-Za-z._+-]*$`)

// shellKind is the kind of shell the program prog is, notShell for none.
func shellKind(prog string) int {
	base := filepath.Base(prog)
	if isZsh(base) {
		return zshKind
	}
	kind, longest := notShell, 0
	for name, k := range shellNames {
		rest, ok := strings.CutPrefix(base, name)
		if ok && len(name) > longest && (rest == "" || shellVersion.MatchString(rest)) {
			kind, longest = k, len(name)
		}
	}
	return kind
}

// posixFlags are the options the shells of posixShell all take with no
// value; -o takes one, as bash's does.
const posixFlags = "abcefhiklmnprstuvxBCDEGHUX"

// shellWord tells whether the word w of a command may be a shell of
// kind it runs, and not its argument: the program itself (first), a name
// with no directory, or a path in a bin directory. A path elsewhere is
// taken for a shell of bash's kin and a zsh as before, but not for one of
// the others: screen -c /tmp/rc runs no rc.
func shellWord(kind int, w string, first bool) bool {
	switch {
	case kind == notShell:
		return false
	case first, kind == bashShell, kind == zshKind, !strings.Contains(w, "/"):
		return true
	}
	dir := filepath.Base(filepath.Dir(w))
	return dir == "bin" || dir == "sbin"
}

// strangeOpts tells whether the options among args, the words after the
// name of a shell of kind, are ones shellArgs may not read as that shell
// does: one made at run time, any of a shell of another language, which
// may run code of its own (fish -C CMD, nu -e CMD), a long one or one
// that is none of posixFlags of a shell of the ksh and ash kin (ksh93 -R
// FILE). The operands after them, a file the shell runs, are none.
func strangeOpts(kind int, args []string, static []bool) bool {
	if kind != posixShell && kind != foreignShell {
		return false
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case !static[i]:
			return true
		case a == "--" || a == "-", len(a) < 2 || a[0] != '-' && a[0] != '+':
			return false
		case kind == foreignShell, strings.HasPrefix(a, "--"):
			return true
		}
		for _, r := range a[1:] {
			switch {
			case r == 'o':
				i++
			case !strings.ContainsRune(posixFlags, r):
				return true
			}
		}
	}
	return false
}

// passwdFile is where the policy finds the login shell of the user su,
// runuser, sudo -i and run0 run code as.
var passwdFile = "/etc/passwd"

// loginShell is the login shell of user, a name or #UID, in passwdFile:
// /bin/sh for an empty one. ok is false when the file does not have the
// user or cannot be read; the other databases of NSS (LDAP, sssd) are
// not asked.
func loginShell(user string) (sh string, ok bool) {
	f, err := os.Open(passwdFile)
	if err != nil {
		return "", false
	}
	defer f.Close()
	uid, byUID := strings.CutPrefix(user, "#")
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Split(sc.Text(), ":")
		if len(fields) != 7 || byUID && fields[2] != uid || !byUID && fields[0] != user {
			continue
		}
		if fields[6] == "" {
			return "/bin/sh", true
		}
		return fields[6], true
	}
	return "", false
}

// ranAs returns code, which the login shell of user runs, read as ranBy
// reads it: the shell passwdFile names, and that of SHELL, which the
// policy took it for before and which su -m runs. A user the file does not
// have, or one not known before the line runs, has a shell the policy
// does not know, and the code is computed.
func (p *parser) ranAs(user string, known bool, code ...string) []string {
	code = p.ranByLogin(code...)
	if len(code) == 0 {
		return code
	}
	sh, ok := "", false
	if known {
		sh, ok = loginShell(user)
	}
	if !ok {
		p.mark(dynComputed)
		return code
	}
	return p.ranBy(sh, code...)
}

// suUser is the user su and runuser without -u run their code as, from
// args, the words after their name: the first operand that is not "-",
// root when there is none. known tells that it is static.
func suUser(args []string, static []bool) (user string, known bool) {
	_, ops := suOpts.read(args)
	if len(ops) > 0 && args[ops[0]] == "-" {
		ops = ops[1:]
	}
	if len(ops) == 0 {
		return "root", true
	}
	return args[ops[0]], static[ops[0]]
}

// loginOf tells whose login shell the wrapper name, with opts read from
// its words, runs the code of its shell with (see wrapped): that of the
// user of -u, root by default, for sudo -i and run0, which runs it with
// --via-shell, -i and with no command. ok is false for a wrapper that
// runs the shell of SHELL alone: sudo -s, doas -s, chroot.
func loginOf(name string, opts []option, static []bool) (user string, known, ok bool) {
	if name != "run0" && (name != "sudo" || !has(opts, "i", "login")) {
		return "", false, false
	}
	user, known = "root", true
	for _, o := range opts {
		if o.name == "u" || o.name == "user" {
			user, known = o.value, o.word < len(static) && static[o.word]
		}
	}
	return user, known, true
}
