package policy

import (
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// commandVars hold a command that programs run, through sh -c or as it is:
// git runs GIT_SSH_COMMAND for ssh and PAGER for its output, git commit and
// crontab -e run EDITOR, sudo -A runs SUDO_ASKPASS, kubectl edit and diff
// KUBE_EDITOR and KUBECTL_EXTERNAL_DIFF, podman compose the program of
// PODMAN_COMPOSE_PROVIDER with its words; so are those of dbxVars, whose
// values distrobox runs or evals. A static value of theirs is
// parsed as the code of bash -c is; one made at run time is computed.
var commandVars = map[string]bool{
	"BROWSER": true, "EDITOR": true, "FCEDIT": true, "GIT_ASKPASS": true, "GIT_EDITOR": true,
	"GIT_EXTERNAL_DIFF": true, "GIT_PAGER": true, "GIT_PROXY_COMMAND": true, "GIT_SEQUENCE_EDITOR": true,
	"GIT_SSH": true, "GIT_SSH_COMMAND": true, "KUBECTL_EXTERNAL_DIFF": true, "KUBE_EDITOR": true,
	"LESSCLOSE": true, "LESSOPEN": true, "MANPAGER": true, "PAGER": true, "PODMAN_COMPOSE_PROVIDER": true,
	"RSYNC_RSH": true, "SSH_ASKPASS": true, "SUDO_ASKPASS": true, "SUDO_EDITOR": true,
	"SYSTEMD_EDITOR": true, "SYSTEMD_PAGER": true, "VISUAL": true,
}

// loaderVars have programs load code from where they say: a library into
// every program (LD_PRELOAD), a module (PYTHONPATH, NODE_OPTIONS=--require),
// key bindings (INPUTRC), the translations of $"…", whose $(…) bash runs
// (TEXTDOMAINDIR), the config of git, which runs commands of its own
// (core.sshCommand, an alias with !), that of kubectl and its kin, whose
// users run the programs of exec (KUBECONFIG, and the aliases of KUBERC),
// that of docker, which runs the helpers of credsStore and the plugins of
// its directories (DOCKER_CONFIG), the files of compose, which hold the
// commands of its services (COMPOSE_FILE, and COMPOSE_ENV_FILES, which may
// name others), the arguments of rg, whose --pre runs a program
// (RIPGREP_CONFIG_PATH), the config of screen, whose shell and exec
// run (SCREENRC), those of podman and its kin, which run the runtime,
// conmon, hooks and mount program they name (CONTAINERS_CONF,
// CONTAINERS_CONF_OVERRIDE, CONTAINERS_STORAGE_CONF), and their helpers from
// CONTAINERS_HELPER_BINARY_DIR, that of nerdctl, which runs the CNI plugins
// of its cni_path (NERDCTL_TOML), as the options --cni-path and
// --cni-netconfpath do (CNI_PATH, NETCONFPATH), and the directory of the
// configs of git, podman and distrobox, which sources its own as shell
// code (XDG_CONFIG_HOME). The code is in files, or in git's
// syntax: whatever the value, they are rebind. So are GIT_CONFIG_KEY_n and
// GIT_CONFIG_VALUE_n (see loads).
var loaderVars = map[string]bool{
	"CNI_PATH": true, "COMPOSE_ENV_FILES": true, "COMPOSE_FILE": true, "CONTAINERS_CONF": true, "CONTAINERS_CONF_OVERRIDE": true,
	"CONTAINERS_HELPER_BINARY_DIR": true, "CONTAINERS_STORAGE_CONF": true,
	"DOCKER_CONFIG": true, "GCONV_PATH": true, "GIT_CONFIG_COUNT": true, "GIT_CONFIG_GLOBAL": true, "GIT_CONFIG_PARAMETERS": true,
	"GIT_CONFIG_SYSTEM": true, "GIT_EXEC_PATH": true, "GIT_TEMPLATE_DIR": true, "INPUTRC": true, "KUBECONFIG": true, "KUBERC": true,
	"LD_AUDIT": true, "LD_LIBRARY_PATH": true, "LD_PRELOAD": true, "NERDCTL_TOML": true, "NETCONFPATH": true,
	"NODE_OPTIONS": true, "NODE_PATH": true,
	"PERL5DB": true, "PERL5LIB": true, "PERL5OPT": true, "PERLLIB": true, "PYTHONHOME": true,
	"PYTHONPATH": true, "PYTHONSTARTUP": true, "RIPGREP_CONFIG_PATH": true, "RUBYLIB": true, "RUBYOPT": true,
	"SCREENRC": true, "TEXTDOMAIN": true, "TEXTDOMAINDIR": true, "XDG_CONFIG_HOME": true,
}

// loads tells whether the variable name has programs load code: see
// loaderVars.
func loads(name string) bool {
	return loaderVars[name] || strings.HasPrefix(name, "GIT_CONFIG_KEY_") || strings.HasPrefix(name, "GIT_CONFIG_VALUE_")
}

// envFunc tells whether the variable name holds a function a bash started
// with it in its environment defines: BASH_FUNC_NAME%%, as export -f puts
// it there, or BASH_FUNC_NAME() of the bash of some distributions.
func envFunc(name string) bool {
	rest, ok := strings.CutPrefix(name, "BASH_FUNC_")
	return ok && (strings.HasSuffix(rest, "%%") || strings.HasSuffix(rest, "()"))
}

// assignedTo marks an assignment of value, static, to the variable name:
// the code a variable of commandVars holds is parsed after the walk of
// the line, with the code the line hands to shells, and that code runs in
// the modes one of optionVars lists (see startsIn); so is the function of
// one of envFunc. Whatever the name, the code of the subscripts in value
// runs when it is read as arithmetic (see value), and all of it in
// ${name@P} (see prompt): of x=…, for x in …, ${x:=…} and env x=… alike.
func (p *parser) assignedTo(name, value string) {
	p.subscript(value)
	p.shown(name, value)
	if p.startsIn(name, value) {
		return
	}
	if envFunc(name) {
		p.envFunction(value)
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

// envFunction looks at value, static, of a variable of envFunc. When it
// starts with "() {", bash defines the function NAME of the code NAME
// VALUE and runs it in place of the command NAME: that is rebind, and the
// code is parsed after the walk of the line, as that of eval is, with f
// for NAME, which bash may take where the parser here does not. Code that
// does not parse is computed too.
func (p *parser) envFunction(value string) {
	p.mark(dynRebind)
	if !strings.HasPrefix(value, "() {") {
		// bash imports no function of it.
		return
	}
	src := "f " + value
	if _, err := upToError(src); err != nil {
		p.mark(dynComputed)
	}
	p.varCode = append(p.varCode, snippet{src, p.remote})
}

// assignedText marks an assignment to the variable name of a value made at
// run time, value its source form, that a wrapper puts in the environment
// of its command: what is written out in it is read as a static value of
// assignedTo is, its subscripts when read as arithmetic and all of it in
// ${name@P}. Which parts of the word were $'…' is not known here, so the
// text is taken both as it is and with the escapes decoded: the \x24( of
// env $'x=a[\x24(id)]' is $( to bash, but the \x5c of a single-quoted part
// stays four characters. Where one part is $'…' and another is not, a \x24
// that is $ and a \x5c that stays as written make a $( that neither form
// shows, so a third form decodes the $ and backtick escapes alone (see
// dollarEscapes).
func (p *parser) assignedText(name, value string) {
	p.assigned(name)
	for _, v := range []string{value, ansiC(value)} {
		p.subscript(v)
		p.shown(name, v)
	}
	p.subscript(dollarEscapes(value))
	// A subscript that reads a variable the line sets runs the code of its
	// value: env "x=a[$y]" with y='$(id)' runs id.
	p.subIndex(value)
}

// varCode is the code programs run from value, that of the variable name
// of commandVars: BROWSER is a list of commands split at colons, LESSOPEN
// starts with the | of a pipe and the - of a command that reads stdin too.
func varCode(name, value string) []string {
	if code, ok := dbxCode(name, value); ok {
		return code
	}
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
