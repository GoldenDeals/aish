package shellstate

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
)

// Zsh is the Kind of a zsh's state. Its values are zsh code: a variable's
// is the line that sets it again, a function's its definition, an alias's
// its value (a global one under "-g NAME", a suffix one under "-s NAME"),
// an option's `setopt NAME` or `unsetopt NAME`.
const Zsh = "zsh"

// zshIgnored are the parameters zsh keeps itself, that belong to the
// terminal, change by themselves, or are tables of zsh/parameter and other
// modules rather than variables of the session.
var zshIgnored = map[string]bool{
	"ARGC": true, "CPUTYPE": true, "EGID": true, "GID": true, "HOST": true, "MACHTYPE": true,
	"OSTYPE": true, "VENDOR": true, "TTY": true, "TTYIDLE": true, "USERNAME": true, "OPTARG": true,
	"TRY_BLOCK_ERROR": true, "TRY_BLOCK_INTERRUPT": true, "ERRNO": true, "EPOCHSECONDS": true,
	"EPOCHREALTIME": true, "MATCH": true, "MBEGIN": true, "MEND": true, "REPLY": true,
	"ZSH_ARGZERO": true, "ZSH_EVAL_CONTEXT": true, "ZSH_EXECUTION_STRING": true, "ZSH_NAME": true,
	"ZSH_PATCHLEVEL": true, "ZSH_SCRIPT": true, "ZSH_SUBSHELL": true, "ZSH_VERSION": true,
	"argv": true, "status": true, "pipestatus": true, "signals": true, "dirstack": true,
	"match": true, "mbegin": true, "mend": true, "reply": true, "epochtime": true,
	"errnos": true, "sysparams": true, "mapfile": true, "langinfo": true,
	"aliases": true, "galiases": true, "saliases": true, "functions": true, "functions_source": true,
	"commands": true, "nameddirs": true, "userdirs": true, "usergroups": true, "options": true,
	"parameters": true, "modules": true, "builtins": true, "reswords": true, "patchars": true,
	"widgets": true, "keymaps": true, "termcap": true, "terminfo": true, "history": true,
	"historywords": true, "jobdirs": true, "jobstates": true, "jobtexts": true, "funcstack": true,
	"funcfiletrace": true, "funcsourcetrace": true, "functrace": true, "zsh_eval_context": true,
	"zsh_scheduled_events": true,
}

// zshIgnoredOpts are the options of how the shell started, which no
// script sets, and those of a function's scope.
var zshIgnoredOpts = map[string]bool{
	"interactive": true, "login": true, "shinstdin": true, "singlecommand": true,
	"privileged": true, "restricted": true, "zle": true, "localoptions": true,
	"localtraps": true, "localpatterns": true,
}

// zshTied are the scalars zsh ties to arrays itself: they are set by their
// array, which keeps them tied.
var zshTied = map[string]bool{
	"PATH": true, "FPATH": true, "CDPATH": true, "MANPATH": true, "MAILPATH": true,
	"MODULE_PATH": true, "PSVAR": true, "FIGNORE": true, "WATCH": true,
}

var validName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ParseZsh reads what __aish_dump of init.zsh printed: `typeset -p`, NUL,
// the names of the special parameters, NUL, functions as name NUL body NUL
// up to an empty name, aliases as name NUL value NUL up to an empty name,
// then options as name NUL on|off. Variables matching a pattern in ignore
// are left out, as Parse does.
func ParseZsh(dump []byte, cwd string, ignore []string) (State, error) {
	typeset, rest, ok := bytes.Cut(dump, []byte{0})
	if !ok {
		return State{}, fmt.Errorf("zsh state: truncated dump")
	}
	specialList, rest, ok := bytes.Cut(rest, []byte{0})
	if !ok {
		return State{}, fmt.Errorf("zsh state: truncated dump")
	}
	special := map[string]bool{}
	for _, n := range strings.Fields(string(specialList)) {
		special[n] = true
	}
	st := State{Kind: Zsh, Vars: map[string]string{}, Funcs: map[string]string{}, Aliases: map[string]string{},
		Opts: map[string]string{}, Cwd: cwd}

	lines := strings.Split(string(typeset), "\n")
	tools := ""
	for _, line := range lines {
		if v, ok := strings.CutPrefix(line, "export AISH_TOOLS_PATH="); ok {
			tools = v
		}
	}
	for _, line := range lines {
		name, code, ok := zshVar(line, special[lineName(line)])
		if !ok || ignored(name) || zshIgnored[name] || secret(name, ignore) {
			continue
		}
		if name == "PATH" && tools != "" {
			// The tools of this aish, which another one puts there itself.
			code = strings.Replace(code, "=( "+tools+" ", "=( ", 1)
		}
		st.Vars[name] = code
	}

	fields := bytes.Split(rest, []byte{0})
	i := 0
	pairs := func(each func(name, value string)) {
		for i+1 < len(fields) && len(fields[i]) > 0 {
			each(string(fields[i]), string(fields[i+1]))
			i += 2
		}
		i++ // the empty name
	}
	pairs(func(name, body string) {
		if !ignored(name) {
			st.Funcs[name] = zshQuote(name) + " () {\n" + body + "\n}"
		}
	})
	pairs(func(name, value string) {
		_, bare, _ := strings.Cut(name, " ")
		if !strings.HasPrefix(name, "-") {
			bare = name
		}
		if !ignored(bare) {
			st.Aliases[name] = value
		}
	})
	if i > len(fields) {
		return State{}, fmt.Errorf("zsh state: truncated aliases")
	}
	for ; i+1 < len(fields); i += 2 {
		name, on := string(fields[i]), string(fields[i+1])
		if zshIgnoredOpts[name] || name == "" {
			continue
		}
		if on == "on" {
			st.Opts[name] = "setopt " + name
		} else {
			st.Opts[name] = "unsetopt " + name
		}
	}
	return st, nil
}

// lineName is the name of the parameter a line of typeset -p sets, for
// the scalar of a tied pair the scalar's.
func lineName(line string) string {
	_, rest, _ := strings.Cut(line, " ")
	for strings.HasPrefix(rest, "-") {
		_, rest, _ = strings.Cut(rest, " ")
	}
	name, _, _ := strings.Cut(rest, "=")
	name, _, _ = strings.Cut(name, " ")
	return name
}

// zshVar reads a line of typeset -p: its name and the code that sets the
// parameter again, global as it is sourced from a function. Not a
// variable: a readonly one, the array of a tied pair (its scalar sets
// both), a line of no parameter. A parameter other than a special one is
// unset first: a variable of the same name in the shell may have
// attributes (an integer, an array) the value does not take, while a
// special one unset would lose what it does.
func zshVar(line string, special bool) (name, code string, ok bool) {
	cmd, rest, found := strings.Cut(line, " ")
	if !found || cmd != "typeset" && cmd != "export" {
		return "", "", false
	}
	var flags []string
	tied := false
	for strings.HasPrefix(rest, "-") {
		var flag string
		flag, rest, _ = strings.Cut(rest, " ")
		if strings.Contains(flag, "r") {
			return "", "", false
		}
		// -g: typeset -p in a function says a parameter is not its own.
		if flag = strings.ReplaceAll(flag, "g", ""); flag == "-" {
			continue
		}
		if strings.Contains(flag, "T") {
			if strings.Contains(flag, "a") {
				return "", "", false
			}
			tied = true
		}
		flags = append(flags, flag)
	}
	name, value, _ := strings.Cut(rest, "=")
	array := ""
	if tied {
		name, array, _ = strings.Cut(name, " ")
	}
	if !validName.MatchString(name) || value == "" && !strings.Contains(rest, "=") {
		return "", "", false
	}
	if tied && zshTied[name] {
		// path=( ... ): the array of the pair zsh keeps.
		return name, array + "=" + value, true
	}
	var b strings.Builder
	if !special {
		b.WriteString("unset -v " + name)
		if tied {
			b.WriteString(" " + array)
		}
		b.WriteString(" 2>/dev/null; ")
	}
	b.WriteString("typeset -g")
	if cmd == "export" {
		b.WriteString(" -x")
	}
	for _, f := range flags {
		b.WriteString(" " + f)
	}
	b.WriteString(" " + rest)
	return name, b.String(), true
}

var zshPlainName = regexp.MustCompile(`^[A-Za-z0-9_.:+@][A-Za-z0-9_.:+@-]*$`)

// zshQuote is name as a word, quoted only if it has to be.
func zshQuote(name string) string {
	if zshPlainName.MatchString(name) {
		return name
	}
	return quote(name)
}

// zshParse are the options that change how zsh reads code: off while the
// script runs, they come back as they were before the session's own.
var zshParse = []string{"aliases", "ignorebraces", "ignoreclosebraces", "kshglob", "shglob", "rcquotes", "cshjunkiequotes"}

// zshScript is zsh that makes the change d in the shell that sources it,
// from __aish_precmd, a function: variables are declared global. The
// user's aliases would expand in a function body as it is read, and their
// options may change how the code here reads: zsh's own syntax is in force
// up to the session's options.
func zshScript(d State) string {
	var b strings.Builder
	// Read with the user's aliases still on: every word quoted, so that
	// none, a global one neither, takes it. __aish_so is a local of
	// __aish_precmd.
	b.WriteString("__aish_so=(")
	for _, o := range zshParse {
		fmt.Fprintf(&b, " '%[1]s' \"${options[%[1]s]}\"", o)
	}
	b.WriteString(" )\n\\setopt")
	for _, o := range zshParse {
		b.WriteString(" 'no" + o + "'")
	}
	b.WriteString("\n")
	for _, name := range keys(d.Vars) {
		code := d.Vars[name]
		if code == "" {
			b.WriteString("unset -v " + name + " 2>/dev/null\n")
			continue
		}
		b.WriteString(code + "\n")
		if name == "PATH" {
			b.WriteString("[[ -n ${AISH_TOOLS_PATH-} ]] && PATH=$AISH_TOOLS_PATH:$PATH\n")
		}
	}
	for _, name := range keys(d.Funcs) {
		if def := d.Funcs[name]; def != "" {
			b.WriteString(def + "\n")
		} else {
			b.WriteString("unfunction -- " + quote(name) + " 2>/dev/null\n")
		}
	}
	for _, key := range keys(d.Aliases) {
		flag, name := "", key
		if f, n, ok := strings.Cut(key, " "); ok && (f == "-g" || f == "-s") {
			flag, name = f+" ", n
		}
		if v := d.Aliases[key]; v != "" {
			b.WriteString("alias " + flag + "-- " + quote(name+"="+v) + "\n")
		} else {
			if flag == "-g " {
				flag = ""
			}
			b.WriteString("unalias " + flag + "-- " + quote(name) + " 2>/dev/null\n")
		}
	}
	// From here on the user's aliases are back: every command is escaped
	// and every word quoted.
	b.WriteString("options+=( \"${__aish_so[@]}\" )\n")
	for _, name := range keys(d.Opts) {
		verb, opt, _ := strings.Cut(d.Opts[name], " ")
		b.WriteString("\\" + verb + " " + quote(opt) + " 2>/dev/null\n")
	}
	b.WriteString("\\unset __aish_so\n")
	if d.Cwd != "" {
		fmt.Fprintf(&b, "[[ $PWD == %[1]s ]] || \\builtin cd -- %[1]s || \\printf 'aish: the session was in %%s\\n' %[1]s\n", quote(d.Cwd))
	}
	return b.String()
}
