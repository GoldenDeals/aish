package policy

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// The policy reads every line as bash does. A line handed to zsh is read
// as bash reads it all the same, which is what the policies know, and zsh
// is watched for where it would read it otherwise: there the commands zsh
// runs are not those the policy found, and the line is computed, or one of
// the kinds bash has for the same thing (a prompt, a name rebound). What
// zsh reads the same — words, quotes, $x, ${x:-y}, $(...), pipes,
// redirections, globs of * ? [...] — goes through the checks of bash.
// zshShell errs on the side of a line marked: a construct it does not know
// is one bash fails to parse, and a line that does not parse is computed.

// NewInputIn is NewInput for a call made in the shell named shell: "zsh"
// reads the line the call hands to it as zsh does, with opts, its options
// on, by zsh's names; any other name is bash's.
func NewInputIn(shell, tool string, args map[string]any, cwd string, env []string, opts ...string) Input {
	if shell != "zsh" {
		return NewInput(tool, args, cwd, env, opts...)
	}
	in := NewInput(tool, args, cwd, env, zshModeOpts(opts)...)
	in.sh.zsh = &zshShell{
		opts:   opts,
		pwd:    in.sh.pwd,
		home:   in.sh.home,
		cdpath: in.sh.cdpath,
		path:   getenv(env, "PATH"),
	}
	return in
}

// zshShell is the zsh a line is handed to: its options on, by zsh's names
// (cdablevars, extendedglob), nil when not known, and what of its
// environment says where a name it takes for a directory is.
type zshShell struct {
	opts                    []string
	pwd, home, cdpath, path string
}

// on tells whether the shell may have option name on: it does, or its
// options are not known.
func (z *zshShell) on(name string) bool {
	return z.opts == nil || slices.Contains(z.opts, name)
}

// zshModeOpts are opts, zsh's options on, as the modes of the parser know
// them by bash's names: cdablevars is cdable_vars, and zsh has no set -k
// (its -k is interactivecomments). zsh reads a comment in the code of eval
// with interactivecomments off too: it is on for the parser.
func zshModeOpts(opts []string) []string {
	if opts == nil {
		return nil
	}
	out := []string{"interactive_comments", "interactive-comments"}
	if slices.Contains(opts, "cdablevars") {
		out = append(out, "cdable_vars")
	}
	return out
}

// zshRange is a glob of numbers, <1-10>, which bash reads as redirections.
var zshRange = regexp.MustCompile(`<[0-9]*-[0-9]*>`)

// zshCode are the commands of zsh that run code from their words or
// later, change how zsh reads a line or what a name runs, or write where
// no redirection says: the policy knows none of them as bash builtins.
var zshCode = map[string]bool{
	"-": true, "alias": true, "autoload": true, "bindkey": true, "clone": true, "disable": true,
	"emulate": true, "enable": true, "fc": true, "foreach": true, "functions": true, "getln": true,
	"hash": true, "nocorrect": true, "noglob": true, "pushln": true, "r": true, "rehash": true,
	"repeat": true, "sched": true, "setopt": true, "sysopen": true, "syswrite": true, "unalias": true,
	"unfunction": true, "unhash": true, "unsetopt": true, "vared": true, "zcompile": true,
	"zcurses": true, "zdelattr": true, "zftp": true, "zle": true, "zmodload": true, "zpty": true,
	"zregexparse": true, "zsetattr": true, "zsocket": true, "zstyle": true, "zsystem": true,
	"ztcp": true, "ztie": true, "zuntie": true,
}

// zshPrompt are the variables whose code zsh runs at a prompt or when a
// command starts, and the functions it calls then: what bash has as
// PROMPT_COMMAND and PS1.
var zshPrompt = map[string]bool{
	"PROMPT": true, "PROMPT2": true, "PROMPT3": true, "PROMPT4": true, "PS1": true, "PS2": true,
	"PS3": true, "PS4": true, "RPROMPT": true, "RPROMPT2": true, "RPS1": true, "RPS2": true,
	"SPROMPT": true, "POSTEDIT": true, "prompt": true, "TMOUT": true, "PERIOD": true,
	"precmd_functions": true, "preexec_functions": true, "chpwd_functions": true,
	"periodic_functions": true, "zshaddhistory_functions": true, "zshexit_functions": true,
}

var zshHooks = map[string]bool{
	"precmd": true, "preexec": true, "chpwd": true, "periodic": true, "zshaddhistory": true,
	"zshexit": true, "command_not_found_handler": true,
}

// zshRebind are the variables that change what a name runs or where zsh
// looks for it: the tables of zsh/parameter, the lowercase arrays tied to
// PATH and FPATH, and the commands zsh runs for a bare redirection.
var zshRebind = map[string]bool{
	"path": true, "PATH": true, "fpath": true, "FPATH": true, "module_path": true, "MODULE_PATH": true,
	"commands": true, "aliases": true, "galiases": true, "saliases": true, "functions": true,
	"dis_aliases": true, "dis_galiases": true, "dis_saliases": true, "dis_functions": true,
	"builtins": true, "dis_builtins": true, "reswords": true, "dis_reswords": true,
	"nameddirs": true, "userdirs": true, "NULLCMD": true, "READNULLCMD": true, "ZDOTDIR": true,
}

// zshComputed are the variables that change how zsh reads what follows or
// write a file: options, the glob characters, mapfile.
var zshComputed = map[string]bool{
	"options": true, "patchars": true, "dis_patchars": true, "mapfile": true, "histchars": true,
	"HISTCHARS": true, "STTY": true, "KEYBOARD_HACK": true,
}

// zshBuiltins are the names zsh runs itself; with autocd a name that is
// none of them nor a program may be a directory zsh goes to.
var zshBuiltins = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`- . : [ alias autoload bg bindkey break builtin bye cd chdir command compadd comparguments compcall compctl compdescribe compfiles compgroups compquote compset comptags comptry compvalues continue declare dirs disable disown echo echotc echoti emulate enable eval exec exit export false fc fg float functions getln getopts hash history integer jobs kill let limit local log logout noglob popd print printf private pushd pushln pwd r read readonly rehash return sched set setopt shift source suspend test times trap true ttyctl type typeset ulimit umask unalias unfunction unhash unlimit unset unsetopt vared wait whence where which zcompile zformat zle zmodload zparseopts zregexparse zstyle ! [[ case coproc do done elif else end esac fi for foreach function if nocorrect repeat select then time until while { }`) {
		zshBuiltins[w] = true
	}
}

// misreads is the kind of dynamic zsh makes of src, code a line hands to
// it, where bash reads src otherwise or does not know what zsh does with
// it; "" when zsh reads it as bash does.
func (z *zshShell) misreads(src string) string {
	if zshRange.MatchString(src) {
		return dynComputed
	}
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(src), "")
	if err != nil {
		// zsh's own syntax: always { }, foreach, ${(e)x}, =(cmd), x &|.
		return dynComputed
	}
	kind := ""
	worst := func(k string) {
		if k == dynComputed || kind == "" {
			kind = k
		}
	}
	quoted := map[*syntax.Word]bool{}
	syntax.Walk(f, func(n syntax.Node) bool {
		if kind == dynComputed {
			return false
		}
		switch n := n.(type) {
		case *syntax.Redirect:
			switch {
			case n.Hdoc != nil:
				quoted[n.Word] = true // the delimiter, which nothing expands
				if quotedWord(n.Word) {
					quoted[n.Hdoc] = true // a body taken as it is written
				}
			case n.Word != nil && (z.word(n.Word, true) || redirMisread(n)):
				worst(dynComputed)
			}
		case *syntax.Word:
			if !quoted[n] && z.word(n, false) {
				worst(dynComputed)
			}
		case *syntax.CallExpr:
			for _, a := range n.Assigns {
				worst(z.assign(a))
			}
			for _, w := range n.Args {
				if z.word(w, true) {
					worst(dynComputed)
				}
			}
			worst(z.call(n))
		case *syntax.DeclClause:
			for _, a := range n.Args {
				worst(z.assign(a))
			}
			if floatDecl(n) {
				worst(dynComputed)
			}
		case *syntax.FuncDecl:
			if name := n.Name.Value; zshHooks[name] || strings.HasPrefix(name, "TRAP") {
				worst(dynPrompt)
			}
		case *syntax.CoprocClause:
			// coproc NAME { } is bash's, zsh runs a command named NAME.
			worst(dynComputed)
		case *syntax.ExtGlob:
			// *(...) is a glob qualifier to zsh, and e:...: runs code.
			worst(dynComputed)
		case *syntax.WordIter:
			for _, w := range n.Items {
				if z.word(w, true) {
					worst(dynComputed)
				}
			}
		}
		return true
	})
	return kind
}

// quotedWord tells whether the delimiter of a here-document is quoted:
// its body is then taken as it is written.
func quotedWord(w *syntax.Word) bool {
	for _, p := range w.Parts {
		switch p := p.(type) {
		case *syntax.Lit:
			if strings.Contains(p.Value, `\`) {
				return true
			}
		default:
			return true
		}
	}
	return false
}

// redirMisread tells whether zsh writes or reads otherwise than bash: >!
// and >>! are a file named ! to bash, and >&p, <&p the coprocess to zsh.
func redirMisread(r *syntax.Redirect) bool {
	w := word(r.Word)
	switch r.Op {
	case syntax.DplOut, syntax.DplIn:
		return w == "p"
	}
	return strings.HasPrefix(w, "!")
}

// word tells whether zsh may make of w other than bash: a $ bash leaves as
// it is ($~x, $=x, $+x), a subscript after $x, adjacent single quotes with
// rc_quotes. An argument also: =cmd, ~name, ~ after = in name=value, the
// globs of extendedglob, braceccl and ***, and with globsubst or nullglob
// an expansion or a glob at all.
func (z *zshShell) word(w *syntax.Word, arg bool) bool {
	parts := w.Parts
	for i, part := range parts {
		switch p := part.(type) {
		case *syntax.Lit:
			if hasDollar(p.Value) || afterParam(parts, i, p.Value) {
				return true
			}
			if arg && z.glob(p.Value, i == 0) {
				return true
			}
			if arg && z.on("magicequalsubst") && strings.Contains(p.Value, "=~") {
				return true // --prefix=~/x is a path in the home to zsh
			}
		case *syntax.SglQuoted:
			if q, ok := prev(parts, i).(*syntax.SglQuoted); ok && !p.Dollar && !q.Dollar && z.on("rcquotes") {
				return true
			}
		case *syntax.DblQuoted:
			for j, q := range p.Parts {
				if lit, ok := q.(*syntax.Lit); ok && (hasDollar(lit.Value) || afterParam(p.Parts, j, lit.Value)) {
					return true
				}
			}
		case *syntax.ParamExp, *syntax.CmdSubst:
			// Out of quotes, globsubst makes a pattern of the value, whose
			// qualifiers run code.
			if arg && z.on("globsubst") {
				return true
			}
		}
	}
	if !arg || len(parts) == 0 {
		return false
	}
	lit, ok := parts[0].(*syntax.Lit)
	if !ok {
		return false
	}
	s := lit.Value
	switch {
	case strings.HasPrefix(s, "=") && z.on("equals"):
		return true // =ls is the path of ls
	case len(s) > 1 && s[0] == '~' && (s[1] == '_' || isLetter(s[1])):
		return true // a named directory of hash -d
	}
	if name, value, ok := strings.Cut(s, "="); ok && syntax.ValidName(name) && strings.Contains(value, "~") {
		// bash takes name=~/x for an assignment and expands ~, zsh does
		// not without magic_equal_subst.
		return true
	}
	return false
}

// glob tells whether zsh may glob the unquoted text s otherwise than bash.
// first tells that s starts the word.
func (z *zshShell) glob(s string, first bool) bool {
	if strings.Contains(s, "***") {
		return true // follows links out of where it starts
	}
	if z.on("braceccl") && strings.Contains(s, "{") {
		return true
	}
	if (z.on("nullglob") || z.on("cshnullglob")) && expands(s) {
		return true // a glob matching nothing takes its word away
	}
	if !z.on("extendedglob") {
		return false
	}
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '^', '#':
			return true
		case '~':
			if i > 0 || !first {
				return true
			}
		}
	}
	return false
}

// afterParam tells whether the literal s at parts[i] follows a $x: zsh
// takes $x[...] for a subscript, whose arithmetic may run code.
func afterParam(parts []syntax.WordPart, i int, s string) bool {
	if !strings.HasPrefix(s, "[") || i == 0 {
		return false
	}
	pe, ok := parts[i-1].(*syntax.ParamExp)
	return ok && pe.Short
}

// hasDollar tells whether the literal s holds a $ no backslash escapes:
// one bash leaves as it is, $~x or $=x, which zsh expands.
func hasDollar(s string) bool {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '$':
			return true
		}
	}
	return false
}

func prev(parts []syntax.WordPart, i int) syntax.WordPart {
	if i == 0 {
		return nil
	}
	return parts[i-1]
}

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// floatDecl tells whether d is typeset -E or -F with a value: a float zsh
// evaluates as arithmetic, which may run code from a subscript in it, and
// a list of function names to bash.
func floatDecl(d *syntax.DeclClause) bool {
	float, valued := false, false
	for _, a := range d.Args {
		switch {
		case a.Naked && a.Value != nil:
			if opt := word(a.Value); strings.HasPrefix(opt, "-") && strings.ContainsAny(opt, "EF") {
				float = true
			}
		case a.Value != nil || a.Array != nil:
			valued = true
		}
	}
	return float && valued
}

// assign is the kind of dynamic an assignment to a's variable makes.
func (z *zshShell) assign(a *syntax.Assign) string {
	if a.Name == nil {
		return ""
	}
	switch name := a.Name.Value; {
	case zshComputed[name]:
		return dynComputed
	case zshPrompt[name]:
		return dynPrompt
	case zshRebind[name]:
		return dynRebind
	}
	return ""
}

// call is the kind of dynamic zsh makes of the simple command c: one of
// zshCode, behind builtin, command or exec too, print -z and -s, set with
// options other than those that mean the same to zsh, a name or an
// assignment of zsh's, and with autocd a name that may be a directory.
func (z *zshShell) call(c *syntax.CallExpr) string {
	var argv []string
	for _, w := range c.Args {
		if !isStatic(w) {
			break
		}
		argv = append(argv, word(w))
	}
	static := len(argv) == len(c.Args)
	for len(argv) > 0 && (argv[0] == "builtin" || argv[0] == "command" || argv[0] == "exec") {
		exec := argv[0] == "exec"
		argv = argv[1:]
		// command -p, exec -c -l -a NAME: an option taken for a command
		// name is only stricter.
		for len(argv) > 0 && strings.HasPrefix(argv[0], "-") && argv[0] != "-" {
			opt := argv[0]
			argv = argv[1:]
			if opt == "--" {
				break
			}
			if exec && strings.HasSuffix(opt, "a") && len(argv) > 0 {
				argv = argv[1:]
			}
		}
	}
	if len(argv) == 0 {
		return ""
	}
	name, args := argv[0], argv[1:]
	switch {
	case zshCode[name], strings.HasPrefix(name, "zf_"):
		return dynComputed
	case name == "print" && printPushes(args):
		return dynComputed
	case name == "set" && !plainSet(args):
		return dynComputed
	case (name == "integer" || name == "float") && !static:
		// A value made at run time, evaluated as arithmetic.
		return dynComputed
	case name == "integer" || name == "float" || name == "private":
		for _, a := range args {
			n, _, _ := strings.Cut(a, "=")
			if zshComputed[n] || zshPrompt[n] || zshRebind[n] {
				return dynComputed
			}
		}
	case z.on("autocd") && z.maybeDir(name):
		return dynComputed
	}
	return ""
}

// printPushes tells whether print's options put its words on the editing
// buffer (-z) or in history (-s), for the user to run.
func printPushes(args []string) bool {
	for _, a := range args {
		if a == "-" || a == "--" || !strings.HasPrefix(a, "-") {
			return false
		}
		if strings.ContainsAny(a[1:], "zs") {
			return true
		}
	}
	return false
}

// plainSet tells whether set's words are only options that zsh and bash
// both take alike: -e, -u, -x, -v and their names, pipefail, or what
// sets the positional parameters.
func plainSet(args []string) bool {
	names := []string{"errexit", "nounset", "xtrace", "verbose", "pipefail"}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" || a == "-" || !strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "+") {
			return true // the positional parameters
		}
		for _, c := range a[1:] {
			switch {
			case c == 'o':
				// set -o alone lists the options.
				if i+1 < len(args) {
					i++
					if !slices.Contains(names, args[i]) {
						return false
					}
				}
			case !strings.ContainsRune("euxv", c):
				return false
			}
		}
	}
	return true
}

// maybeDir tells whether zsh with autocd may take the command name for a
// directory to go to: a directory it names from the shell's directory, or
// with CDPATH; a name with no program in PATH nor a builtin of zsh, which
// may name one of a cdpath the environment does not show.
func (z *zshShell) maybeDir(name string) bool {
	if strings.Contains(name, "=") {
		return false
	}
	isDir := func(p string) bool {
		st, err := os.Stat(p)
		return err == nil && st.IsDir()
	}
	switch {
	case name == "~" || strings.HasPrefix(name, "~/"):
		return z.home == "" || isDir(z.home+name[1:])
	case strings.HasPrefix(name, "~"):
		return true
	case filepath.IsAbs(name):
		return isDir(name)
	case strings.Contains(name, "/"):
		return z.pwd == "" || isDir(filepath.Join(z.pwd, name))
	}
	if z.pwd == "" || isDir(filepath.Join(z.pwd, name)) {
		return true
	}
	for _, d := range filepath.SplitList(z.cdpath) {
		if d != "" && isDir(filepath.Join(d, name)) {
			return true
		}
	}
	if zshBuiltins[name] {
		return false
	}
	for _, d := range filepath.SplitList(z.path) {
		if d == "" {
			d = z.pwd
		}
		if st, err := os.Stat(filepath.Join(d, name)); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return false
		}
	}
	return true
}

// A line the shell runs as bash may hand code to a zsh all the same: one
// it starts by name (zsh -c, the stdin of zsh, su -s /bin/zsh -c) or the
// shell of SHELL when that names a zsh, which sudo -s, su, script, flock
// -c, the windows of tmux and screen and the like run their code with.
// That code, and the code it hands on in turn, is read as a zsh may read
// it too, as a line of a zsh the shell is: the parser keeps its text and
// parses it with the misreads of a zsh whose options, those of its rc
// files, are not known (see zshShell.on). A zsh started so by a zsh the
// shell is has the options of its rc files too, not those of the shell.
// SHELL unset or naming another shell is taken for bash, as before.

// zshes are the zshes a line starts.
type zshes struct {
	// child is a zsh the line starts, in the shell's directory and with
	// its HOME, CDPATH and PATH.
	child zshShell
	// login tells that SHELL names a zsh.
	login bool
	// code holds the code they run, by its text.
	code map[string]bool
}

func newZshes(sh shell) zshes {
	return zshes{
		child: zshShell{pwd: sh.pwd, home: sh.home, cdpath: sh.cdpath, path: sh.path},
		login: isZsh(sh.login),
	}
}

// isZsh tells whether the program prog is a zsh: its file named zsh, zsh5,
// zsh-5.9..., as the shell of the config is told (shells.For).
func isZsh(prog string) bool {
	return strings.HasPrefix(filepath.Base(prog), "zsh")
}

// zshFor is the zsh that runs src, code handed on by code cur runs, nil
// for bash: a zsh the line starts, or cur, which runs the code of its own
// code that the line does not hand to a zsh.
func (p *parser) zshFor(src string, cur *zshShell) *zshShell {
	if p.zshes.code[src] {
		return &p.zshes.child
	}
	return cur
}

// ranBy returns code, which the shell prog runs: of a zsh, it is read as
// one the line starts reads it.
func (p *parser) ranBy(prog string, code ...string) []string {
	if isZsh(prog) {
		p.zshRuns(code)
	}
	return code
}

// ranByLogin returns code, which the shell of SHELL runs, read as ranBy
// reads it.
func (p *parser) ranByLogin(code ...string) []string {
	if p.zshes.login {
		p.zshRuns(code)
	}
	return code
}

func (p *parser) zshRuns(code []string) {
	if p.zshes.code == nil {
		p.zshes.code = map[string]bool{}
	}
	for _, c := range code {
		p.zshes.code[c] = true
	}
}

// startedBy returns code, which the command name of strung or logins runs
// with a shell of its own, read as ranBy reads it: su and runuser run it
// with the shell of -s, else with the login shell of the user they run
// it as, which the policy takes for SHELL; flock -c, script, sg and
// newgrp with SHELL too; watch with sh.
func (p *parser) startedBy(name string, args []string, code ...string) []string {
	switch name {
	case "su", "runuser":
		opts, _ := suOpts.read(args)
		if sh := values(opts, "s", "shell"); len(sh) > 0 {
			return p.ranBy(sh[len(sh)-1].text, code...)
		}
		return p.ranByLogin(code...)
	case "flock", "script", "sg", "newgrp":
		return p.ranByLogin(code...)
	}
	return code
}

// shellRunners run the code runs finds of theirs with the shell of SHELL:
// tmux and screen in their windows (default-shell), ssh, scp and sftp the
// ProxyCommand and LocalCommand of this machine and sftp its !, Vim :! and
// system(), at and batch their jobs.
var shellRunners = map[string]bool{
	"at": true, "batch": true, "ex": true, "gvim": true, "nvim": true, "scp": true, "screen": true,
	"sftp": true, "ssh": true, "tmux": true, "vi": true, "view": true, "vim": true, "vimdiff": true,
}

// runBy returns code, that the runners among argv run here, read as
// ranByLogin reads it when one of them is of shellRunners.
func (p *parser) runBy(argv []string, code []string) []string {
	if slices.ContainsFunc(argv, func(a string) bool { return shellRunners[filepath.Base(a)] }) {
		return p.ranByLogin(code...)
	}
	return code
}
