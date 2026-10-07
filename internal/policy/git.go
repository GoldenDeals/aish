package policy

import (
	"slices"
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
// the line; --exec-path=DIR has git run its commands from DIR. The commands
// of gitCode run code of their words, and an alias of -c may be one.
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
		default:
			if f := gitCode[w.args[cmd]]; f != nil {
				rs = append(rs, f(w, cmd+1)...)
			}
		}
	}
	// An alias runs at the top of the repository, but for one that runs a
	// command of git, which runs here.
	return append(elsewhere(rs), gitAlias(w, opts, cmd)...)
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
// which runs here for a repository here, and the hooks of --template. They
// read their options among their operands: git clone "$u" may be any.
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
	// A word made at run time may be any of these options.
	s := whole(w).from(from)
	return append(rs, s.at(gitLoose(s.w, cloneOpts, opts, false))...)
}

// optCode maps the names of the options of a command to what of their
// value is code, as gitConfigCode does the variables of git.
type optCode map[string]func(value string) (code run, ok bool)

// gitCode are the commands of git 2.55 that run code their words hold, and
// what finds it among their words from word from on: the line for sh -c of
// rebase --exec, difftool --extcmd, grep -O (the files after it), daemon
// --access-hook and instaweb --httpd; the upload-pack of fetch, pull,
// ls-remote and fetch-pack, the receive-pack of push and send-pack and the
// upload-archive of archive --exec, which git runs with sh -c for a
// repository here; the tool of difftool -t (see toolCode); the commands of
// filter-branch, submodule foreach, bisect run, mergetool and send-email.
// But for bisect and the scripts filter-branch, submodule and mergetool
// they read their options among their operands, and a word made at run
// time anywhere may be one of them.
var gitCode = map[string]func(w words, from int) []run{
	"archive":       gitOpts(archiveOpts, optCode{"exec": gitShell}),
	"bisect":        gitBisect,
	"daemon":        gitOpts(daemonOpts, optCode{"access-hook": gitShell}),
	"difftool":      gitOpts(difftoolOpts, optCode{"extcmd": gitShell, "t": toolCode, "tool": toolCode, "x": gitShell}),
	"fetch":         gitOpts(fetchOpts, optCode{"upload-pack": gitShell}),
	"fetch-pack":    gitOpts(fetchPackOpts, optCode{"exec": gitShell, "upload-pack": gitShell}),
	"filter-branch": gitFilterBranch,
	"grep":          gitOpts(grepOpts, optCode{"O": gitShell, "open-files-in-pager": gitShell}),
	"instaweb":      gitOpts(instawebOpts, optCode{"d": gitShell, "httpd": gitShell}),
	"ls-remote":     gitOpts(lsRemoteOpts, optCode{"exec": gitShell, "upload-pack": gitShell}),
	"mergetool":     gitMergetool,
	"pull":          gitOpts(pullOpts, optCode{"upload-pack": gitShell}),
	"push":          gitOpts(pushOpts, optCode{"exec": gitShell, "receive-pack": gitShell}),
	"rebase":        gitOpts(rebaseOpts, optCode{"exec": gitShell, "x": gitShell}),
	"send-email":    gitSendEmail,
	"send-pack":     gitOpts(sendPackOpts, optCode{"exec": gitShell, "receive-pack": gitShell}),
	"submodule":     gitSubmodule,
}

// The options of the commands of gitCode that take a value, as git 2.55
// reads them, for the values not to be taken for options: an option left
// out only has its value read as a word of its own. daemon and fetch-pack
// take a value only after "=".
var (
	archiveOpts  = getopt{short: "o:", long: "format: prefix: add-file: add-virtual-file: output: mtime: remote: exec:"}
	daemonOpts   = getopt{long: "access-hook::"}
	difftoolOpts = getopt{short: "t:x:", long: "tool: extcmd:"}
	fetchOpts    = getopt{
		short: "j:o:",
		long: `upload-pack: jobs: recurse-submodules:: recurse-submodules-default: submodule-prefix: depth: deepen:
			shallow-since: shallow-exclude: refmap: server-option: negotiation-restrict: negotiation-tip:
			negotiation-include: filter:`,
	}
	fetchPackOpts = getopt{long: "upload-pack:: exec::"}
	grepOpts      = getopt{
		short: "A:B:C:e:f:m:O::",
		long:  "max-depth: color:: context: before-context: after-context: threads: open-files-in-pager:: max-count:",
	}
	instawebOpts = getopt{short: "b:d:m:p:", long: "browser: httpd: module-path: port:"}
	lsRemoteOpts = getopt{short: "o:", long: "upload-pack: exec: sort: server-option:"}
	pullOpts     = getopt{
		short: "j::o:r::S::s:X:",
		long: `recurse-submodules:: rebase:: log:: signoff:: cleanup: strategy: strategy-option: gpg-sign:: upload-pack:
			jobs:: depth: deepen: shallow-since: shallow-exclude: refmap: server-option: negotiation-restrict:
			negotiation-tip: negotiation-include:`,
	}
	pushOpts   = getopt{short: "o:", long: "repo: force-with-lease:: recurse-submodules: receive-pack: exec: signed:: push-option:"}
	rebaseOpts = getopt{
		short: "C:r::S::s:X:x:",
		long:  "onto: trailer: whitespace: empty: gpg-sign:: exec: rebase-merges:: strategy: strategy-option:",
	}
	sendPackOpts = getopt{long: "receive-pack: exec: remote: signed:: push-option: force-with-lease::"}
)

// gitOpts finds the code of a command of git that reads its options as g
// does (see gitFind).
func gitOpts(g getopt, code optCode) func(w words, from int) []run {
	return func(w words, from int) []run {
		s := whole(w).from(from)
		opts, _ := g.read(s.w.args)
		return s.at(gitFind(s.w, g, opts, code, false))
	}
}

// gitFind returns the code of the options opts a command has read of w as
// g does: what code finds in their values, any for a value made at run
// time, and the words made at run time that may be options (see gitLoose).
func gitFind(w words, g getopt, opts []option, code optCode, plus bool) []run {
	rs := gitLoose(w, g, opts, plus)
	for _, o := range opts {
		f := code[o.name]
		switch {
		case f == nil:
		case !w.static[o.word]:
			rs = append(rs, run{words: []int{o.word}})
		default:
			if r, ok := f(o.value); ok {
				r.words = []int{o.word}
				rs = append(rs, r)
			}
		}
	}
	return rs
}

// gitLoose returns a run that hangs on each word made at run time that may
// be an option of a command reading its options as g does among its
// operands, before a "--" that is no value, as optLoose does for the
// program; but a word that may split does so even as the value of an
// option: its other words come after the value. plus tells that "+" starts
// an option too, as for Getopt::Long.
func gitLoose(w words, g getopt, opts []option, plus bool) []run {
	var lw []int
	isHeld := held(g, opts, w.args)
	for k, a := range w.args {
		if w.static[k] {
			if a == "--" && !valued(opts, w.args, k) {
				break
			}
			continue
		}
		c, ok := firstChar(a)
		if (w.optionLike(k) || plus && ok && c == '+') && (w.split[k] || !isHeld(k)) {
			lw = append(lw, k)
		}
	}
	if lw == nil {
		return nil
	}
	return []run{{words: lw}}
}

// toolCode is the tool of git difftool -t and mergetool -t: a name with a
// "/" in it is a path from the directory of the tools of git, and git
// sources the file there.
func toolCode(v string) (run, bool) {
	return run{mark: dynComputed}, strings.Contains(v, "/")
}

// gitBisect finds the command of git bisect run, whose words git runs as
// they are. A subcommand made at run time may be run.
func gitBisect(w words, from int) []run {
	switch {
	case from >= len(w.args):
		return nil
	case !w.static[from]:
		return []run{{words: []int{from}}}
	case w.args[from] != "run" || from+1 == len(w.args):
		return nil
	}
	return []run{w.argv(indexes(from+1, len(w.args)))}
}

// gitSubmodule finds the command of git submodule foreach, a script that
// takes -q, --quiet and --cached before its command and -q, --quiet and
// --recursive after foreach, then hands the first word after them to sh -c
// as a line and the rest to it as its arguments. A command made at run
// time may be foreach.
func gitSubmodule(w words, from int) []run {
	k := gitSkip(w, from, "-q", "--quiet", "--cached")
	switch {
	case k == len(w.args):
		return nil
	case !w.static[k]:
		return []run{{words: []int{k}}}
	case w.args[k] != "foreach":
		return nil
	}
	if k = gitSkip(w, k+1, "-q", "--quiet", "--recursive"); k == len(w.args) {
		return nil
	}
	r := w.argv(indexes(k+1, len(w.args)))
	r.text = w.args[k] + " " + r.text
	r.words = append(r.words, k)
	return []run{r}
}

// gitSkip returns the index of the first word of w from from on that is
// none of flags.
func gitSkip(w words, from int, flags ...string) int {
	for from < len(w.args) && w.static[from] && slices.Contains(flags, w.args[from]) {
		from++
	}
	return from
}

// filterCode are the options of git filter-branch whose value it evals.
var filterCode = map[string]bool{
	"--commit-filter": true, "--env-filter": true, "--index-filter": true, "--msg-filter": true,
	"--parent-filter": true, "--setup": true, "--tag-name-filter": true, "--tree-filter": true,
}

// gitFilterBranch finds the code of git filter-branch, a script that reads
// its options up to the first word that is none: each but -f, --force,
// --prune-empty and --remap-to-ancestor takes the next word whole, and it
// evals those of filterCode. A word made at run time among the options may
// be any of them, and so may the words of a value that splits.
func gitFilterBranch(w words, from int) []run {
	var rs []run
	for k := from; k < len(w.args); k++ {
		a := w.args[k]
		switch {
		case !w.static[k]:
			if w.optionLike(k) {
				rs = append(rs, run{words: []int{k}})
			}
			return rs
		case a == "--" || !strings.HasPrefix(a, "-"):
			return rs
		case a == "-f" || a == "--force" || a == "--prune-empty" || a == "--remap-to-ancestor":
			continue
		}
		k++
		switch {
		case k == len(w.args):
		case filterCode[a]:
			rs = append(rs, run{text: w.args[k], words: []int{k}})
		case w.split[k] && w.optionLike(k):
			rs = append(rs, run{words: []int{k}})
		}
	}
	return rs
}

// gitMergetool finds the tool of git mergetool (see toolCode), a script
// that reads its options up to the first word that is none: -t, and --tool
// or any word that starts with it but --tool-help, takes the value after
// "=" or the next word.
func gitMergetool(w words, from int) []run {
	var rs []run
	for k := from; k < len(w.args); k++ {
		a := w.args[k]
		switch {
		case !w.static[k]:
			if w.optionLike(k) {
				rs = append(rs, run{words: []int{k}})
			}
			return rs
		case a == "--" || !strings.HasPrefix(a, "-"):
			return rs
		case a != "-t" && (!strings.HasPrefix(a, "--tool") || strings.HasPrefix(a, "--tool-help")):
			continue
		}
		_, v, eq := strings.Cut(a, "=")
		if !eq {
			if k++; k == len(w.args) {
				return rs
			}
			v = w.args[k]
		}
		switch r, ok := toolCode(v); {
		case !w.static[k]:
			rs = append(rs, run{words: []int{k}})
		case ok:
			rs = append(rs, r)
		}
	}
	return rs
}

// sendEmailOpts are the options of git send-email that take a value, as
// sendEmailRead reads them: one of "::" only after "=".
var sendEmailOpts = getopt{long: `8bit-encoding: batch-size: bcc: cc: cc-cmd: compose-encoding: confirm: envelope-sender:
	from: header-cmd: identity: imap-sent-folder: in-reply-to: relogin-delay: reply-to: sender: sendmail-cmd: smtp-auth:
	smtp-debug:: smtp-domain:: smtp-encryption: smtp-pass:: smtp-server: smtp-server-option: smtp-server-port:
	smtp-ssl-cert-path: smtp-ssl-client-cert: smtp-ssl-client-key: smtp-user: subject: suppress-cc: to: to-cmd:
	transfer-encoding: v:`}

// sendEmailCode are the options of git send-email whose value it runs: the
// commands of recipients, of headers and of sendmail, with sh -c, and the
// program at the absolute path of --smtp-server.
var sendEmailCode = optCode{
	"cc-cmd": gitShell, "header-cmd": gitShell, "sendmail-cmd": gitShell, "smtp-server": pathCode, "to-cmd": gitShell,
}

// gitSendEmail finds the code of git send-email (see sendEmailCode).
func gitSendEmail(w words, from int) []run {
	s := whole(w).from(from)
	opts := sendEmailRead(s.w.args)
	return s.at(gitFind(s.w, sendEmailOpts, opts, sendEmailCode, true))
}

// sendEmailRead reads the options of git send-email as its Getopt::Long
// does, among the operands up to "--": after "--", "-" or "+", by its name
// in any case or a prefix of no other name, a value after "=" or in the
// next word, whatever that is.
func sendEmailRead(args []string) []option {
	var opts []option
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, long := strings.CutPrefix(a, "--")
		switch {
		case a == "--":
			return opts
		case long:
		case len(a) > 1 && (a[0] == '-' || a[0] == '+'):
			name = a[1:]
		default:
			continue
		}
		name, v, eq := strings.Cut(name, "=")
		o := option{value: v, word: i}
		var takes int
		if o.name, takes = sendEmailOpts.longOpt(strings.ToLower(name)); takes == 1 && !eq && i+1 < len(args) {
			i++
			o.value, o.word = args[i], i
		}
		opts = append(opts, o)
	}
	return opts
}

// gitAlias finds what the alias -c sets last for the command at cmd runs,
// when it is no line for the shell (see aliasCode): git with its words
// before the command, the words of the alias as git splits them and the
// words after the command, which may be an alias in turn or a command of
// gitCode: git -c alias.r=rebase r -x CMD.
func gitAlias(w words, opts []option, cmd int) []run {
	if cmd == len(w.args) || !w.static[cmd] {
		return nil
	}
	value, found := "", false
	for _, o := range opts {
		key, v, _ := strings.Cut(o.value, "=")
		section, name, _ := strings.Cut(key, ".")
		if o.name == "c" && w.static[o.word] && strings.EqualFold(section, "alias") && strings.EqualFold(name, w.args[cmd]) {
			value, found = v, true
		}
	}
	args, ok := splitCmdline(value)
	if !found || !ok || strings.HasPrefix(value, "!") {
		return nil
	}
	for i, a := range args {
		args[i] = quoted(a)
	}
	head, tail := w.argv(indexes(0, cmd)), w.argv(indexes(cmd+1, len(w.args)))
	text := "git " + head.text + " " + strings.Join(args, " ") + " " + tail.text
	return []run{{text: text, words: append(head.words, tail.words...)}}
}

// splitCmdline splits the value of an alias into words as git does: at
// blanks out of quotes, which it takes away, a backslash out of single
// quotes keeping the character after it. ok is false for a value git
// refuses: a quote left open or a backslash at the end.
func splitCmdline(s string) (args []string, ok bool) {
	var b strings.Builder
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote == 0 && strings.IndexByte(" \t\n\r", c) >= 0:
			args = append(args, b.String())
			b.Reset()
			for i+1 < len(s) && strings.IndexByte(" \t\n\r", s[i+1]) >= 0 {
				i++
			}
		case quote == 0 && (c == '\'' || c == '"'):
			quote = c
		case c == quote:
			quote = 0
		default:
			if c == '\\' && quote != '\'' {
				if i++; i == len(s) {
					return nil, false
				}
				c = s[i]
			}
			b.WriteByte(c)
		}
	}
	return append(args, b.String()), quote == 0
}
