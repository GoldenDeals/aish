package policy

import "strings"

// gitConfigCode are the variables of git config whose value git runs as
// code, keyed by section and name in lower case, "*" for any name: what of
// the value is a line for a shell, false for none. git -c, --config-env and
// git config look them up here; core.sshCommand, core.pager and the other
// variables of code go here too.
var gitConfigCode = map[string]func(value string) (string, bool){
	"alias.*": aliasCode,
}

// aliasCode is the code of an alias of git: after a "!" a line for sh -c,
// else a command of git, whose options before it git reads as its own; -c
// among them may hold code in turn.
func aliasCode(v string) (string, bool) {
	if code, ok := strings.CutPrefix(v, "!"); ok {
		return code, true
	}
	if strings.HasPrefix(v, "-") {
		return "git " + v, true
	}
	return "", false
}

// gitVar finds what of the value of a variable of git, named
// section[.subsection].name, is code; nil for a variable of no code.
func gitVar(key string) func(string) (string, bool) {
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
// its -c sets for the command, and that git config keeps in a file, marked
// rebind: a name of a command of git runs it later. --config-env takes the
// value of a variable from the environment, out of the line.
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
				if code, ok := f(value); ok {
					rs = append(rs, run{text: code, words: []int{o.word}})
				}
			}
		case "config-env":
			if key, _, _ := strings.Cut(o.value, "="); !w.static[o.word] || gitVar(key) != nil {
				rs = append(rs, run{words: []int{o.word}, mark: dynComputed})
			}
		}
	}
	if cmd < len(w.args) && w.static[cmd] && w.args[cmd] == "config" {
		rs = append(rs, gitConfig(w, cmd+1)...)
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
				} else if code, ok := f(w.args[j]); ok {
					rs = append(rs, run{text: code, words: []int{k, j}, mark: dynRebind})
				}
			}
		}
	}
	return rs
}
