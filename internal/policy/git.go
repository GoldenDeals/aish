package policy

import (
	"strconv"
	"strings"
)

// gitConfigCode are the variables of git config whose value git runs as
// code, keyed by section and name in lower case (a subsection left out),
// "*" for any name: what of the value is a line for a shell (text), or the
// mark of code it names and the line does not hold (computed for a file of
// config, a directory of hooks); ok is false for a value of no code. git
// -c, --config-env, clone -c and git config look them up here.
var gitConfigCode = map[string]func(value string) (code run, ok bool){
	"alias.*":                    aliasCode,
	"browser.cmd":                gitShell,
	"browser.path":               gitShell,
	"core.alternaterefscommand":  gitShell,
	"core.askpass":               gitShell,
	"core.editor":                gitShell,
	"core.fsmonitor":             unlessBool,
	"core.gitproxy":              proxyCode,
	"core.hookspath":             fileCode,
	"core.pager":                 gitShell,
	"core.sshcommand":            gitShell,
	"credential.helper":          credentialCode,
	"diff.command":               gitShell,
	"diff.external":              gitShell,
	"diff.textconv":              gitShell,
	"difftool.cmd":               gitShell,
	"difftool.path":              gitShell,
	"filter.clean":               gitShell,
	"filter.process":             gitShell,
	"filter.smudge":              gitShell,
	"gpg.defaultkeycommand":      gitShell,
	"gpg.program":                gitShell,
	"guitool.cmd":                gitShell,
	"hook.command":               gitShell,
	"include.path":               fileCode,
	"includeif.path":             fileCode,
	"init.templatedir":           fileCode,
	"instaweb.httpd":             gitShell,
	"interactive.difffilter":     gitShell,
	"man.cmd":                    gitShell,
	"man.path":                   gitShell,
	"merge.driver":               gitShell,
	"mergetool.cmd":              gitShell,
	"mergetool.path":             gitShell,
	"pager.*":                    unlessBool,
	"protocol.allow":             protocolCode,
	"remote.receivepack":         gitShell,
	"remote.uploadpack":          gitShell,
	"sendemail.cccmd":            gitShell,
	"sendemail.headercmd":        gitShell,
	"sendemail.sendmailcmd":      gitShell,
	"sendemail.smtpserver":       pathCode,
	"sendemail.tocmd":            gitShell,
	"sequence.editor":            gitShell,
	"submodule.update":           bangCode,
	"tar.command":                gitShell,
	"uploadpack.packobjectshook": gitShell,
}

// aliasCode is the code of an alias of git: after a "!" a line for sh -c,
// else a command of git, whose options before it git reads as its own; -c
// among them may hold code in turn.
func aliasCode(v string) (run, bool) {
	if code, ok := strings.CutPrefix(v, "!"); ok {
		return run{text: code}, true
	}
	if strings.HasPrefix(v, "-") {
		return run{text: "git " + v}, true
	}
	return run{}, false
}

// gitShell is a value git runs with sh -c, or as a program with its
// arguments; empty, it runs none.
func gitShell(v string) (run, bool) {
	return run{text: v}, v != ""
}

// unlessBool is gitShell of a value that may be a boolean instead, as git
// reads one: a pager of pager.CMD, a hook of core.fsmonitor.
func unlessBool(v string) (run, bool) {
	switch strings.ToLower(v) {
	case "", "true", "false", "yes", "no", "on", "off":
		return run{}, false
	}
	if _, err := strconv.Atoi(v); err == nil {
		return run{}, false
	}
	return gitShell(v)
}

// proxyCode is the command of core.gitProxy, which may end in " for
// DOMAIN".
func proxyCode(v string) (run, bool) {
	cmd, _, _ := strings.Cut(v, " for ")
	return gitShell(cmd)
}

// bangCode is the line for a shell after the "!" of submodule.NAME.update.
func bangCode(v string) (run, bool) {
	code, ok := strings.CutPrefix(v, "!")
	return run{text: code}, ok
}

// pathCode is the program of sendemail.smtpServer, an absolute path.
func pathCode(v string) (run, bool) {
	return run{text: quoted(v)}, strings.HasPrefix(v, "/")
}

// credentialCode is the helper of credential.helper: a line for the shell
// after a "!", a path with its arguments, or else a name git credential-
// runs, which git hands to the shell too.
func credentialCode(v string) (run, bool) {
	switch {
	case v == "":
		return run{}, false
	case strings.HasPrefix(v, "!"):
		return run{text: v[1:]}, true
	case strings.HasPrefix(v, "/"):
		return run{text: v}, true
	}
	return run{text: "git credential-" + v}, true
}

// fileCode is a file or a directory git reads config or hooks from: any
// code may be there.
func fileCode(v string) (run, bool) {
	return run{mark: dynComputed}, v != ""
}

// protocolCode is protocol.allow and protocol.NAME.allow: one but never
// may let ext:: run a command of a URL.
func protocolCode(v string) (run, bool) {
	return run{mark: dynComputed}, !strings.EqualFold(v, "never")
}

// gitVar finds what of the value of a variable of git, named
// section[.subsection].name, is code; nil for a variable of no code.
func gitVar(key string) func(string) (run, bool) {
	section, rest, ok := strings.Cut(key, ".")
	if !ok {
		return nil
	}
	section, name := strings.ToLower(section), strings.ToLower(rest[strings.LastIndexByte(rest, '.')+1:])
	if f := gitConfigCode[section+"."+name]; f != nil {
		return f
	}
	return gitConfigCode[section+".*"]
}

// gitRead reads the options of git before its command as git 2.51 does: -C,
// -c and the long options of a value take the next word for it unless it is
// after "=" in theirs, and the first word that is no option is the command.
func gitRead(args []string) (opts []option, cmd int) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			return opts, i
		}
		name, v, _ := strings.Cut(a, "=")
		o := option{name: strings.TrimLeft(name, "-"), value: v, word: i}
		switch a {
		case "-C", "-c", "--git-dir", "--work-tree", "--namespace", "--super-prefix", "--config-env", "--attr-source":
			if i+1 == len(args) {
				return opts, len(args)
			}
			i++
			o.value, o.word = args[i], i
		}
		opts = append(opts, o)
	}
	return opts, len(args)
}

// gitRuns finds the code of git: that of the variables of gitConfigCode
// its -c sets for the command, and that git config and git clone -c keep
// in a file, marked rebind: a name of a command of git runs it later.
// --config-env takes the value of a variable from the environment, out of
// the line; --exec-path=DIR has git run its commands from DIR.
func gitRuns(w words) []run {
	opts, cmd := gitRead(w.args)
	var rs []run
	end := cmd
	if cmd < len(w.args) && !w.static[cmd] {
		// The command made at run time may be an option.
		end = cmd + 1
	}
	if lw := w.loose(end, held(getopt{short: "+C:c:"}, opts, w.args)); len(lw) > 0 {
		rs = append(rs, run{words: lw})
	}
	for _, o := range opts {
		switch o.name {
		case "c":
			key, value, _ := strings.Cut(o.value, "=")
			switch f := gitVar(key); {
			case !w.static[o.word]:
				rs = append(rs, run{words: []int{o.word}})
			case f != nil:
				if r, ok := f(value); ok {
					r.words = []int{o.word}
					rs = append(rs, r)
				}
			}
		case "config-env":
			if key, _, _ := strings.Cut(o.value, "="); !w.static[o.word] || gitVar(key) != nil {
				rs = append(rs, run{words: []int{o.word}, mark: dynComputed})
			}
		case "exec-path":
			if strings.Contains(w.args[o.word], "=") {
				rs = append(rs, run{mark: dynRebind})
			}
		}
	}
	if cmd < len(w.args) && w.static[cmd] {
		switch w.args[cmd] {
		case "config":
			rs = append(rs, gitConfig(w, cmd+1)...)
		case "clone", "init":
			rs = append(rs, gitClone(w, cmd+1)...)
		}
	}
	// An alias runs at the top of the repository.
	return elsewhere(rs)
}

// gitConfig finds the code git config writes: the value of a variable of
// gitConfigCode, any word after its name, which a name of a command of git
// runs later. A word made at run time may be such a name or value.
func gitConfig(w words, from int) []run {
	var rs []run
	for k := from; k < len(w.args); k++ {
		f := gitVar(w.args[k])
		switch {
		case !w.static[k]:
			if k+1 < len(w.args) {
				rs = append(rs, run{words: []int{k}})
			}
		case f != nil:
			for j := k + 1; j < len(w.args); j++ {
				if !w.static[j] {
					rs = append(rs, run{words: []int{j}}, run{mark: dynRebind})
				} else if r, ok := f(w.args[j]); ok {
					r.words = []int{k, j}
					rs = append(rs, r, run{mark: dynRebind})
				}
			}
		}
	}
	return rs
}

// The options of git clone and git init 2.51 that take a value.
var cloneOpts = getopt{
	short: "j:o:b:u:c:",
	long: `jobs: template: reference: reference-if-able: origin: branch: revision: upload-pack: depth: shallow-since:
		shallow-exclude: separate-git-dir: ref-format: config: server-option: filter: bundle-uri: recurse-submodules::
		recursive:: object-format: initial-branch: shared::`,
}

// gitClone finds the code of git clone and git init from word from on:
// the variables of gitConfigCode clone -c sets in the new repository,
// which run as it fetches and later, the shell command of --upload-pack,
// which runs here for a repository here, and the hooks of --template.
func gitClone(w words, from int) []run {
	opts, _ := cloneOpts.read(w.args[from:])
	var rs []run
	for _, o := range opts {
		k := from + o.word
		switch o.name {
		case "c", "config":
			key, value, _ := strings.Cut(o.value, "=")
			switch f := gitVar(key); {
			case !w.static[k]:
				rs = append(rs, run{words: []int{k}})
			case f != nil:
				if r, ok := f(value); ok {
					r.words = []int{k}
					rs = append(rs, r, run{mark: dynRebind})
				}
			}
		case "u", "upload-pack":
			rs = append(rs, run{text: o.value, words: []int{k}})
		case "template":
			rs = append(rs, run{words: []int{k}, mark: dynComputed})
		}
	}
	return rs
}
