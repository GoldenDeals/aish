package policy

import (
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// commandVars hold a command that programs run, through sh -c or as it is:
// git runs GIT_SSH_COMMAND for ssh and PAGER for its output, git commit and
// crontab -e run EDITOR, sudo -A runs SUDO_ASKPASS. A static value of theirs
// is parsed as the code of bash -c is; one made at run time is computed.
var commandVars = map[string]bool{
	"BROWSER": true, "EDITOR": true, "FCEDIT": true, "GIT_ASKPASS": true, "GIT_EDITOR": true,
	"GIT_EXTERNAL_DIFF": true, "GIT_PAGER": true, "GIT_PROXY_COMMAND": true, "GIT_SEQUENCE_EDITOR": true,
	"GIT_SSH": true, "GIT_SSH_COMMAND": true, "LESSCLOSE": true, "LESSOPEN": true, "MANPAGER": true,
	"PAGER": true, "RSYNC_RSH": true, "SSH_ASKPASS": true, "SUDO_ASKPASS": true, "SUDO_EDITOR": true,
	"SYSTEMD_EDITOR": true, "SYSTEMD_PAGER": true, "VISUAL": true,
}

// loaderVars have programs load code from where they say: a library into
// every program (LD_PRELOAD), a module (PYTHONPATH, NODE_OPTIONS=--require),
// key bindings (INPUTRC), the config of git, which runs commands of its own
// (core.sshCommand, an alias with !). The code is in files, or in git's
// syntax: whatever the value, they are rebind. So are GIT_CONFIG_KEY_n and
// GIT_CONFIG_VALUE_n (see loads).
var loaderVars = map[string]bool{
	"GCONV_PATH": true, "GIT_CONFIG_COUNT": true, "GIT_CONFIG_GLOBAL": true, "GIT_CONFIG_PARAMETERS": true,
	"GIT_CONFIG_SYSTEM": true, "GIT_EXEC_PATH": true, "GIT_TEMPLATE_DIR": true, "INPUTRC": true,
	"LD_AUDIT": true, "LD_LIBRARY_PATH": true, "LD_PRELOAD": true, "NODE_OPTIONS": true, "NODE_PATH": true,
	"PERL5DB": true, "PERL5LIB": true, "PERL5OPT": true, "PERLLIB": true, "PYTHONHOME": true,
	"PYTHONPATH": true, "PYTHONSTARTUP": true, "RUBYLIB": true, "RUBYOPT": true,
}

// loads tells whether the variable name has programs load code: see
// loaderVars.
func loads(name string) bool {
	return loaderVars[name] || strings.HasPrefix(name, "GIT_CONFIG_KEY_") || strings.HasPrefix(name, "GIT_CONFIG_VALUE_")
}

// assignedTo marks an assignment of value, static, to the variable name:
// the code a variable of commandVars holds is parsed after the walk of
// the line, with the code the line hands to shells, and that code runs in
// the modes one of optionVars lists (see startsIn). Whatever the name, the
// code of the subscripts in value runs when it is read as arithmetic (see
// value), and all of it in ${name@P} (see prompt): of x=…, for x in …,
// ${x:=…} and env x=… alike.
func (p *parser) assignedTo(name, value string) {
	p.subscript(value)
	p.shown(name, value)
	if p.startsIn(name, value) {
		return
	}
	if !commandVars[name] {
		p.assigned(name)
		return
	}
	for _, src := range varCode(name, value) {
		if strings.TrimSpace(src) != "" {
			p.varCode = append(p.varCode, snippet{src, p.remote})
		}
	}
}

// varCode is the code programs run from value, that of the variable name
// of commandVars: BROWSER is a list of commands split at colons, LESSOPEN
// starts with the | of a pipe and the - of a command that reads stdin too.
func varCode(name, value string) []string {
	switch name {
	case "BROWSER":
		return strings.Split(value, ":")
	case "LESSOPEN":
		return []string{strings.TrimPrefix(strings.TrimLeft(value, "|"), "-")}
	}
	return []string{value}
}

// assignedWord marks an assignment of the word w, nil for none, to the
// variable name.
func (p *parser) assignedWord(name string, w *syntax.Word) {
	switch {
	case w == nil:
		p.assignedTo(name, "")
	case isStatic(w):
		p.assignedTo(name, word(w))
	default:
		p.assigned(name)
	}
}

// assignment marks an assignment the parser takes for one: NAME=VALUE
// alone, before a command or of declare and its kin, NAME+=VALUE, whose
// value is appended to one not known, and NAME=(VALUES).
func (p *parser) assignment(a *syntax.Assign) {
	name := a.Name.Value
	switch {
	case a.Append:
		p.assigned(name)
	case a.Array != nil && len(a.Array.Elems) > 0:
		for _, e := range a.Array.Elems {
			p.assignedWord(name, e.Value)
		}
	case a.Array != nil:
		p.assignedTo(name, "")
	default:
		p.assignedWord(name, a.Value)
	}
}

// declared marks a NAME=VALUE word of declare and its kin, static: a
// NAME+ or a NAME[SUBSCRIPT] as named does.
func (p *parser) declared(name, value string) {
	if base, ok := strings.CutSuffix(name, "+"); ok || strings.Contains(name, "[") {
		p.named(base)
		return
	}
	p.assignedTo(name, value)
}

// iter marks the variable of for and select, which takes each of its words
// in turn, or the positional parameters without in.
func (p *parser) iter(w *syntax.WordIter) {
	if !w.InPos.IsValid() {
		p.assigned(w.Name.Value)
		return
	}
	for _, item := range w.Items {
		p.assignedWord(w.Name.Value, item)
	}
}

// defaulted marks ${NAME=WORD} and ${NAME:=WORD}, which assign WORD to
// NAME unset (or empty); ${!x:=WORD} to the variable x names.
func (p *parser) defaulted(pe *syntax.ParamExp) {
	switch {
	case pe.Exp == nil || pe.Exp.Op != syntax.AssignUnset && pe.Exp.Op != syntax.AssignUnsetOrNull:
	case pe.Excl || pe.Param == nil:
		p.mark(dynComputed)
	default:
		p.assignedWord(pe.Param.Value, pe.Exp.Word)
	}
}
