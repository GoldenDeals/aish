package policy

import (
	"slices"
	"strings"
)

// setters are the builtins that set what the shell runs later, or what a
// name runs: the code of bind -x, complete -C and mapfile -C runs when a key
// is pressed, a word is completed or lines are read, and is parsed as that
// of trap is; hash -p, enable and unset PATH make a name of a command run
// another program; read, printf -v, mapfile, getopts, wait -p and declare
// and its kin run as a command (behind builtin or command) set variables,
// PS1 and PATH among them; fc runs commands of the history; set -k makes
// NAME=VALUE words of the commands after it assignments (see modesOf). A word of
// theirs made at run time may be any option or name: they are marked for
// it. let, test and [ are here for the code in the subscripts of their
// strings (see evaluates), as are unset and the names of the others.
var setters = map[string]func(p *parser, args []string, static []bool) []string{
	"[":         (*parser).test,
	"bind":      (*parser).bind,
	"compgen":   (*parser).complete,
	"complete":  (*parser).completeLater,
	"declare":   declares(false),
	"enable":    (*parser).enable,
	"export":    declares(true),
	"fc":        (*parser).fc,
	"getopts":   (*parser).getopts,
	"hash":      (*parser).hash,
	"let":       (*parser).let,
	"local":     declares(false),
	"mapfile":   (*parser).mapfile,
	"printf":    (*parser).printf,
	"read":      (*parser).read,
	"readarray": (*parser).mapfile,
	"readonly":  declares(false),
	"set":       (*parser).set,
	"test":      (*parser).test,
	"typeset":   declares(false),
	"unset":     (*parser).unset,
	"wait":      (*parser).wait,
}

// The options of the setters as bash 5.3 reads them: up to the first
// operand.
var (
	bindOpts     = getopt{short: "+lvpVPsSXf:q:u:m:r:x:"}
	completeOpts = getopt{short: "+abcdefgjko:prsuvA:C:F:G:P:S:V:W:X:DEI"}
	enableOpts   = getopt{short: "+adnpsf:"}
	fcOpts       = getopt{short: "+e:lnrs"}
	getoptsOpts  = getopt{short: "+"}
	hashOpts     = getopt{short: "+dlp:rt"}
	mapfileOpts  = getopt{short: "+d:u:n:O:tC:c:s:"}
	printfOpts   = getopt{short: "+v:"}
	readOpts     = getopt{short: "+Eersa:d:i:n:p:t:u:N:"}
	unsetOpts    = getopt{short: "+fnv"}
	waitOpts     = getopt{short: "+fnp:"}
)

// bind returns the commands of bind -x, which bash runs when the key is
// pressed, and marks a key bound to a macro: text typed for the user, a
// newline in it runs a command; bind -f reads bindings of both from a file.
// Either is code kept for later.
func (p *parser) bind(args []string, static []bool) []string {
	if slices.Contains(static, false) {
		p.mark(dynComputed)
		p.deferred()
		return nil
	}
	opts, ops := bindOpts.read(args)
	if has(opts, "f") {
		p.mark(dynComputed) // an inputrc: macros and commands of a file
		p.deferred()
	}
	var code []string
	for _, x := range values(opts, "x") {
		p.deferred()
		cmds := bindCommands(x.text)
		if cmds == nil {
			p.mark(dynComputed)
		}
		code = append(code, cmds...)
	}
	for _, i := range ops {
		if macro(args[i]) {
			p.mark(dynComputed)
			p.deferred()
		}
	}
	return code
}

// bindCommands finds the command of a bind -x spec, "KEYSEQ": COMMAND: the
// key sequence in double quotes, a colon, then the command, up to the end
// or in quotes of its own. Bash 5.3 takes a blank for the colon too, 5.2
// takes the first colon after the key sequence: what either runs is
// returned. nil when it is not a spec.
func bindCommands(spec string) []string {
	_, end, ok := isolate(spec, 0, true)
	if !ok {
		return nil
	}
	var cmds []string
	for _, sep := range []string{":", ": \t"} {
		c := strings.IndexAny(spec[end:], sep)
		if c < 0 {
			continue
		}
		if cmd, _, ok := isolate(spec, end+c+1, false); ok && !slices.Contains(cmds, cmd) {
			cmds = append(cmds, cmd)
		}
	}
	return cmds
}

// isolate reads a part of a bind spec from i as bash's isolate_sequence
// does: after blanks, text in double or, unless dquote, single quotes, in
// which a backslash keeps the next character, or else the rest of s. It
// returns the text and the index of the closing quote, len(s) for none.
func isolate(s string, i int, dquote bool) (text string, end int, ok bool) {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	var delim byte
	switch {
	case i < len(s) && (s[i] == '"' || !dquote && s[i] == '\''):
		delim = s[i]
		i++
	case dquote:
		return "", 0, false
	}
	start := i
	for ; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case delim:
			return s[start:i], i, true
		}
	}
	if delim != 0 {
		return "", 0, false
	}
	return s[start:], len(s), true
}

// macro tells whether a line of bind binds a key to a macro: KEY: "TEXT".
func macro(line string) bool {
	from := 0
	if _, end, ok := isolate(line, 0, true); ok {
		from = end
	}
	c := strings.IndexByte(line[from:], ':')
	if c < 0 {
		return false
	}
	v := strings.TrimLeft(line[from+c+1:], " \t")
	return strings.HasPrefix(v, `"`) || strings.HasPrefix(v, "'")
}

// complete returns the command of complete -C and compgen -C, which bash
// runs to complete a word, and marks a wordlist of -W that bash expands
// then, $(…) among its words. compgen -V sets a variable.
func (p *parser) complete(args []string, static []bool) []string {
	if slices.Contains(static, false) {
		p.mark(dynComputed)
		return nil
	}
	opts, _ := completeOpts.read(args)
	var code []string
	for _, o := range opts {
		switch o.name {
		case "C":
			code = append(code, o.value)
		case "W":
			if wordsRun(o.value) {
				p.mark(dynComputed)
			}
		case "V":
			p.named(o.value)
		}
	}
	return code
}

// wordsRun tells whether bash may run code expanding a wordlist of
// complete -W or compgen -W.
func wordsRun(v string) bool {
	return strings.ContainsAny(v, "$`") || strings.Contains(v, "<(") || strings.Contains(v, ">(")
}

// completeLater is complete, which keeps what it is given for later: the
// command of -C, the function of -F and the wordlist of -W run when a word
// is completed. compgen runs them at once.
func (p *parser) completeLater(args []string, static []bool) []string {
	opts, _ := completeOpts.read(args)
	if slices.Contains(static, false) || has(opts, "C", "F") || slices.ContainsFunc(values(opts, "W"), func(w piece) bool { return wordsRun(w.text) }) {
		p.deferred()
	}
	return p.complete(args, static)
}

// declares looks at the words of export (export) or of declare, local,
// readonly and typeset run as a command, behind builtin or command, which
// the parser takes for no assignments.
func declares(export bool) func(*parser, []string, []bool) []string {
	return func(p *parser, args []string, static []bool) []string {
		nameref := false
		for i, a := range args {
			if !static[i] {
				p.mark(dynComputed)
				continue
			}
			p.declWord(a, export, &nameref)
		}
		return nil
	}
}

// enable marks a builtin it loads from a library, with -f or from
// BASH_LOADABLES_PATH for a name it has no builtin of, or enables: the name
// runs it instead of the program it ran. -n and -d take builtins away, -p
// and no names list them.
func (p *parser) enable(args []string, static []bool) []string {
	if slices.Contains(static, false) {
		p.mark(dynRebind)
		return nil
	}
	switch opts, ops := enableOpts.read(args); {
	case has(opts, "f"):
		p.mark(dynRebind)
	case has(opts, "n", "d", "p"), len(ops) == 0:
	default:
		p.mark(dynRebind)
	}
	return nil
}

// fc runs commands of the history, which this line does not hold: edited,
// or again with -s or -e -. Only fc -l without -s lists them.
func (p *parser) fc(args []string, static []bool) []string {
	if opts, _ := fcOpts.read(args); !has(opts, "l") || has(opts, "s", "e") || slices.Contains(static, false) {
		p.mark(dynComputed)
	}
	return nil
}

// getopts OPTSTRING NAME sets NAME to the option it reads.
func (p *parser) getopts(args []string, static []bool) []string {
	if _, ops := getoptsOpts.read(args); len(ops) > 1 {
		if !static[ops[1]] {
			p.mark(dynComputed)
		} else {
			p.named(args[ops[1]])
		}
	}
	return nil
}

// hash -p makes a name run the file it gives, whatever PATH holds.
func (p *parser) hash(args []string, static []bool) []string {
	if opts, _ := hashOpts.read(args); has(opts, "p") || slices.Contains(static, false) {
		p.mark(dynRebind)
	}
	return nil
}

// mapfile returns the callback of -C, which bash runs as it reads lines,
// and marks the array it sets.
func (p *parser) mapfile(args []string, static []bool) []string {
	if slices.Contains(static, false) {
		p.mark(dynComputed)
		return nil
	}
	opts, ops := mapfileOpts.read(args)
	for _, i := range ops {
		p.named(args[i])
	}
	var code []string
	for _, c := range values(opts, "C") {
		code = append(code, c.text)
	}
	return code
}

// printf -v sets a variable. Only its first words are options: one made at
// run time that may start with - may be -v.
func (p *parser) printf(args []string, static []bool) []string {
	if len(args) > 0 && !static[0] && dashed(args[0]) {
		p.mark(dynComputed)
		return nil
	}
	opts, _ := printfOpts.read(args)
	for _, o := range opts {
		switch {
		case o.name != "v":
		case !static[o.word]:
			p.mark(dynComputed)
		default:
			p.named(o.value)
		}
	}
	return nil
}

// dashed tells whether a word made at run time, in its source form, may
// start with -: it does not start with text that stays as it is, such as
// the "%s: $x" of a format; an expansion, a glob or an escape of $'…' may
// become -.
func dashed(s string) bool {
	if s == "" {
		return true
	}
	switch c := s[0]; {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return false
	case c == '\\':
		// \n of a format stays; \x2d, \055 and - of $'…' are -.
		return len(s) < 2 || strings.IndexByte("xuU01234567", s[1]) >= 0
	}
	return !strings.ContainsRune("%./:,_=#^~ \t\"'", rune(s[0]))
}

// read marks the variables it sets: its names and the array of -a.
func (p *parser) read(args []string, static []bool) []string {
	if slices.Contains(static, false) {
		p.mark(dynComputed)
		return nil
	}
	opts, ops := readOpts.read(args)
	for _, a := range values(opts, "a") {
		p.named(a.text)
	}
	for _, i := range ops {
		p.named(args[i])
	}
	return nil
}

// unset PATH leaves bash looking for a command in the current directory;
// unset HOME, CDPATH and the like change the paths after it (lineVars).
func (p *parser) unset(args []string, static []bool) []string {
	if slices.Contains(static, false) {
		p.mark(dynRebind)
		return nil
	}
	opts, ops := unsetOpts.read(args)
	if has(opts, "f") {
		return nil
	}
	for _, i := range ops {
		switch name, _, _ := strings.Cut(args[i], "["); {
		case name == "PATH":
			p.mark(dynRebind)
		case lineVars[name]:
			// unset HOME makes $HOME/x /x.
			p.mark(dynComputed)
		}
		p.subscript(args[i])
	}
	return nil
}

// wait -p sets a variable to the id of the job it waited for. A number is
// no code: wait "$pid" is not marked for the -p it may be.
func (p *parser) wait(args []string, static []bool) []string {
	opts, _ := waitOpts.read(args)
	for _, o := range opts {
		switch {
		case o.name != "p":
		case !static[o.word]:
			p.mark(dynComputed)
		default:
			p.named(o.value)
		}
	}
	return nil
}
