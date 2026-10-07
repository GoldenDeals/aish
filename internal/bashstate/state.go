// Package bashstate is what a session keeps of the shell itself: variables,
// functions, aliases, options and the working directory. init.bash dumps
// them at every prompt; a session saves only what changed since the shell
// started, and resuming it replays that change in another shell as a script
// the shell sources at its next prompt.
package bashstate

import (
	"bytes"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

// State is a shell's state or, from Diff, a change of it: there an empty
// value means the name is gone.
type State struct {
	Vars    map[string]string `json:"vars,omitempty"`    // name → its `declare -p` line
	Funcs   map[string]string `json:"funcs,omitempty"`   // name → its `declare -f` text
	Aliases map[string]string `json:"aliases,omitempty"` // name → value
	Opts    map[string]string `json:"opts,omitempty"`    // name → `set -o x`, `shopt -s x`...
	Cwd     string            `json:"cwd,omitempty"`
}

// Empty reports whether a change changes nothing.
func (s State) Empty() bool {
	return len(s.Vars)+len(s.Funcs)+len(s.Aliases)+len(s.Opts) == 0 && s.Cwd == ""
}

// Variables the shell keeps itself, that belong to the terminal or to this
// aish rather than to the session.
var ignoredVars = map[string]bool{
	"BASH": true, "BASHOPTS": true, "BASHPID": true, "COLUMNS": true, "LINES": true,
	"COMP_WORDBREAKS": true, "DIRSTACK": true, "EPOCHREALTIME": true, "EPOCHSECONDS": true,
	"EUID": true, "UID": true, "PPID": true, "GROUPS": true, "HISTCMD": true, "LINENO": true,
	"OLDPWD": true, "PWD": true, "PIPESTATUS": true, "RANDOM": true, "SRANDOM": true,
	"SECONDS": true, "SHLVL": true, "SHELLOPTS": true, "FUNCNAME": true, "OPTIND": true,
	"OPTERR": true, "HOSTNAME": true, "HOSTTYPE": true, "MACHTYPE": true, "OSTYPE": true,
}

var ignoredOpts = map[string]bool{"login_shell": true, "restricted_shell": true}

// ignored are names private to aish and to shell plugins (bash-completion
// loads hundreds of `_` functions on demand), and the shell's own.
func ignored(name string) bool {
	return strings.HasPrefix(name, "_") || strings.HasPrefix(name, "AISH_") ||
		strings.HasPrefix(name, "BASH_") || strings.HasPrefix(name, "READLINE_") ||
		ignoredVars[name]
}

// secret reports whether a variable name matches one of the shell patterns
// (`*TOKEN*`, `AWS_*`) the user keeps out of the session's state. A bad
// pattern matches nothing: the config rejects them before they get here.
func secret(name string, ignore []string) bool {
	for _, p := range ignore {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}

var funcHeader = regexp.MustCompile(`(?m)^(\S+) \(\) $\n\{ $`)

// Parse reads what __aish_dump printed: `declare -p`, NUL, `declare -f`,
// NUL, alias name/value pairs each ended by NUL, an empty name, then
// `set +o` and `shopt -p`. Variables matching a pattern in ignore are left
// out, so that a state parsed with the same list neither saves nor undoes
// them.
func Parse(dump []byte, cwd string, ignore []string) (State, error) {
	parts := bytes.SplitN(dump, []byte{0}, 3)
	if len(parts) < 3 {
		return State{}, fmt.Errorf("bash state: truncated dump")
	}
	st := State{Vars: map[string]string{}, Funcs: map[string]string{}, Aliases: map[string]string{},
		Opts: map[string]string{}, Cwd: cwd}

	// One variable a line: bash quotes newlines in values as $'\n'.
	lines := strings.Split(string(parts[0]), "\n")
	tools := ""
	for _, line := range lines {
		if v, ok := strings.CutPrefix(line, `declare -x AISH_TOOLS_PATH="`); ok {
			tools = strings.TrimSuffix(v, `"`)
		}
	}
	for _, line := range lines {
		f := strings.SplitN(line, " ", 3)
		if len(f) < 3 || f[0] != "declare" || !strings.HasPrefix(f[1], "-") {
			continue
		}
		name, _, _ := strings.Cut(f[2], "=")
		if ignored(name) || strings.Contains(f[1], "r") || secret(name, ignore) {
			continue // readonly ones cannot be set again
		}
		if name == "PATH" && tools != "" {
			// The tools of this aish, which another one puts there itself.
			line = strings.Replace(line, `="`+tools+":", `="`, 1)
		}
		st.Vars[name] = line
	}

	// Functions are cut at the headers bash prints, `name () ` and `{ `:
	// the bodies may hold anything, here-documents included.
	text := string(parts[1])
	heads := funcHeader.FindAllStringSubmatchIndex(text, -1)
	for i, h := range heads {
		end := len(text)
		if i+1 < len(heads) {
			end = heads[i+1][0]
		}
		name := text[h[2]:h[3]]
		if !ignored(name) {
			st.Funcs[name] = strings.TrimRight(text[h[0]:end], "\n")
		}
	}

	rest := parts[2]
	for {
		name, after, ok := bytes.Cut(rest, []byte{0})
		if !ok {
			return State{}, fmt.Errorf("bash state: truncated aliases")
		}
		rest = after
		if len(name) == 0 {
			break
		}
		value, after, ok := bytes.Cut(rest, []byte{0})
		if !ok {
			return State{}, fmt.Errorf("bash state: truncated aliases")
		}
		rest = after
		if !ignored(string(name)) {
			st.Aliases[string(name)] = string(value)
		}
	}

	for _, line := range strings.Split(string(rest), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 || (f[0] != "set" && f[0] != "shopt") {
			continue
		}
		if !ignoredOpts[f[2]] {
			st.Opts[f[2]] = line
		}
	}
	return st, nil
}

// Diff is the change that turns from into to. The working directory is
// always part of it: a resumed session goes back to where it was.
func Diff(from, to State) State {
	d := State{Vars: diff(from.Vars, to.Vars), Funcs: diff(from.Funcs, to.Funcs),
		Aliases: diff(from.Aliases, to.Aliases), Opts: diff(from.Opts, to.Opts), Cwd: to.Cwd}
	for k, v := range d.Opts {
		if v == "" {
			delete(d.Opts, k) // an option bash no longer has
		}
	}
	return d
}

func diff(from, to map[string]string) map[string]string {
	d := map[string]string{}
	for k, v := range to {
		if from[k] != v {
			d[k] = v
		}
	}
	for k := range from {
		if _, ok := to[k]; !ok {
			d[k] = ""
		}
	}
	return d
}

// Apply is base changed by d.
func Apply(base, d State) State {
	st := State{Vars: apply(base.Vars, d.Vars), Funcs: apply(base.Funcs, d.Funcs),
		Aliases: apply(base.Aliases, d.Aliases), Opts: apply(base.Opts, d.Opts), Cwd: base.Cwd}
	if d.Cwd != "" {
		st.Cwd = d.Cwd
	}
	return st
}

func apply(base, d map[string]string) map[string]string {
	m := make(map[string]string, len(base)+len(d))
	for k, v := range base {
		m[k] = v
	}
	for k, v := range d {
		if v == "" {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	return m
}

// Script is bash that makes the change d in the shell that sources it. It
// is sourced from a function, so variables are declared global.
func Script(d State) string {
	var b strings.Builder
	// Aliases expand while a script is read: a function body that uses one
	// would get it expanded a second time. Under set -k the NAME=VALUE of
	// `declare -g NAME=VALUE` goes to the environment of declare, which sets
	// nothing and lists the variables instead: the option is off while the
	// lines of `declare -p` run as they are, and comes back before the
	// session's options, which may change it.
	b.WriteString("shopt -q expand_aliases && __aish_ea=1 || __aish_ea=\nshopt -u expand_aliases\n")
	b.WriteString("[[ $- == *k* ]] && __aish_kw=1 || __aish_kw=\nset +k\n")
	for _, name := range keys(d.Vars) {
		// In the shell the script runs in the name may be a nameref: unset -v
		// and declare go through it to the variable it refers to, which the
		// script may have set already. unset -n does nothing to a plain
		// variable; a nameref still there after it is a readonly one, left
		// as it is.
		b.WriteString("if [[ -R " + name + " ]]; then unset -n " + name + "; else unset -v " + name + "; fi 2>/dev/null\n")
		if line := d.Vars[name]; line != "" {
			f := strings.SplitN(line, " ", 3)
			flags := "-g"
			if f[1] != "--" {
				flags += strings.TrimPrefix(f[1], "-")
			}
			b.WriteString("[[ -R " + name + " ]] || declare " + flags + " " + f[2] + "\n")
			if name == "PATH" {
				b.WriteString("[[ -n ${AISH_TOOLS_PATH-} ]] && PATH=$AISH_TOOLS_PATH:$PATH\n")
			}
		}
	}
	for _, name := range keys(d.Funcs) {
		if def := d.Funcs[name]; def != "" {
			b.WriteString(def + "\n")
		} else {
			b.WriteString("unset -f " + quote(name) + " 2>/dev/null\n")
		}
	}
	for _, name := range keys(d.Aliases) {
		if v := d.Aliases[name]; v != "" {
			b.WriteString("alias " + quote(name+"="+v) + "\n")
		} else {
			b.WriteString("unalias " + quote(name) + " 2>/dev/null\n")
		}
	}
	b.WriteString("[[ $__aish_ea ]] && shopt -s expand_aliases\n[[ $__aish_kw ]] && set -k\nunset -v __aish_ea __aish_kw\n")
	for _, name := range keys(d.Opts) {
		b.WriteString(d.Opts[name] + " 2>/dev/null\n")
	}
	if d.Cwd != "" {
		fmt.Fprintf(&b, "[[ $PWD == %[1]s ]] || builtin cd -- %[1]s || printf 'aish: the session was in %%s\\n' %[1]s\n", quote(d.Cwd))
	}
	return b.String()
}

func keys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
