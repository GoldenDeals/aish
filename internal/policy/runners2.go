package policy

import (
	"slices"
	"strings"
)

// More of optRunners, read as fd 10, ripgrep 15, at 3.2, OpenSSH 10 and
// Vim 9 read their words: the command of fd -x and -X, the programs of rg
// --pre and --hostname-bin, the shell commands at reads on stdin or from
// the file of -f, the lines of sftp that run a shell here, and the shell
// Vim runs for :! in a command of -c, --cmd or +.

// fdOpts are the options of fd with a value. Those of -x and -X are all
// the words after them up to a ";" (see fdRuns). clap takes no prefix of a
// long name, which the parser here may take for an option: fd fails on it.
var fdOpts = getopt{
	short: "C:E:S:X:c:d:e:j:o:t:x:",
	long: `and: base-directory: batch-size: change-newer-than: change-older-than: changed-after: changed-before:
		changed-within: color: exact-depth: exclude: exec: exec-batch: extension: format: gen-completions::
		hyperlink:: ignore-contain: ignore-file: max-buffer-time: max-depth: max-results: min-depth: newer: older:
		owner: path-separator: search-path: size: strip-cwd-prefix:: threads: type:`,
}

// fdPlaceholders are what fd puts a path, or a part of one, in place of in
// a word of its command.
var fdPlaceholders = []string{"{}", "{/}", "{//}", "{.}", "{/.}"}

// fdRuns finds the commands of fd -x and -X, which fd runs as they are,
// each word with a placeholder in it made at run time; in the directory of
// -C, when it is there. As the program, a word made at run time that may
// be an option marks the line (see optLoose).
func fdRuns(w words, program bool) []run {
	var rs []run
	moved := false
	var loose []int
	for k := 0; k < len(w.args); k++ {
		a := w.args[k]
		if !w.static[k] {
			switch {
			case wrapper{opts: fdOpts}.fixed(a) && strings.HasPrefix(a, "-") && fdExec(a):
				// --exec="$c": a command of one word made at run time.
				rs = append(rs, run{words: []int{k}})
			case program && w.optionLike(k) && !wrapper{opts: fdOpts}.fixed(a):
				loose = append(loose, k)
			}
			continue
		}
		if a == "--" {
			break
		}
		if len(a) < 2 || a[0] != '-' {
			continue
		}
		name, value, attached, next := fdOption(a)
		switch {
		case name == "base-directory":
			moved = true
		case name != "exec":
		case attached:
			// A value in the word is the whole command: fd puts the
			// path after it. An empty one runs nothing.
			if value != "" {
				rs = append(rs, fdCommand(words{[]string{value}, []bool{true}, []bool{false}}, []int{0}))
			}
			continue
		default:
			cmd := fdUpTo(w, k+1)
			rs = append(rs, fdCommand(w, cmd))
			if fdMayEnd(w, cmd) {
				rs = append(rs, run{mark: dynComputed})
			}
			k += len(cmd) + 1
			continue
		}
		if next {
			k++
		}
	}
	if loose != nil {
		rs = append(rs, run{words: loose})
	}
	for i := range rs {
		rs[i].moved = moved
	}
	return rs
}

// fdOption reads a word of options of fd: the name of the last option in
// it, exec for -x, -X and their long names, its value when it is in the
// word, and whether it takes the next word for its value.
func fdOption(a string) (name, value string, attached, next bool) {
	if strings.HasPrefix(a, "--") {
		long, v, eq := strings.Cut(a[2:], "=")
		long, takes := fdOpts.longOpt(long)
		if long == "exec-batch" {
			long = "exec"
		}
		return long, v, eq, takes == 1 && !eq
	}
	for j := 1; j < len(a); j++ {
		if colons(fdOpts.short, a[j]) == 0 {
			continue
		}
		switch name = a[j : j+1]; name {
		case "x", "X":
			name = "exec"
		case "C":
			name = "base-directory"
		}
		// clap takes an "=" after a letter for no part of the value.
		value = strings.TrimPrefix(a[j+1:], "=")
		return name, value, j+1 < len(a), j+1 == len(a)
	}
	return "", "", false, false
}

// fdExec tells whether a word of fd made at run time, whose name is fixed,
// is -x or -X with a value in it.
func fdExec(a string) bool {
	name, _, attached, _ := fdOption(a)
	return name == "exec" && attached
}

// fdUpTo returns the indexes of the words of a command of fd from word i,
// up to a ";" or the end: clap takes every word for a value of -x up to
// it, one with a "-" too.
func fdUpTo(w words, i int) []int {
	var cmd []int
	for k := i; k < len(w.args); k++ {
		if w.static[k] && w.args[k] == ";" {
			break
		}
		cmd = append(cmd, k)
	}
	return cmd
}

// fdCommand is the command of the words idx of w, each word with a
// placeholder in it made at run time.
func fdCommand(w words, idx []int) run {
	v := words{w.args, slices.Clone(w.static), w.split}
	for _, k := range idx {
		if slices.ContainsFunc(fdPlaceholders, func(ph string) bool { return strings.Contains(w.args[k], ph) }) {
			v.static[k] = false
		}
	}
	return v.argv(idx)
}

// fdMayEnd tells whether a word of the command cmd made at run time may be
// the ";" that ends it, with a word after it that fd would then read for
// an option: one with a "-" or one made at run time.
func fdMayEnd(w words, cmd []int) bool {
	for n, k := range cmd {
		if w.static[k] {
			continue
		}
		if c, ok := firstChar(w.args[k]); ok && c != ';' {
			continue
		}
		for _, j := range cmd[n+1:] {
			if !w.static[j] || strings.HasPrefix(w.args[j], "-") {
				return true
			}
		}
	}
	return false
}

// rgOpts are the options of ripgrep with a value.
var rgOpts = getopt{
	short: "A:B:C:E:M:T:d:e:f:g:j:m:r:t:",
	long: `after-context: before-context: color: colors: context: context-separator: dfa-size-limit: encoding: engine:
		field-context-separator: field-match-separator: file: generate: glob: hostname-bin: hyperlink-format: iglob:
		ignore-file: max-columns: max-count: max-depth: max-filesize: path-separator: pre: pre-glob: regex-size-limit:
		regexp: replace: sort: sortr: threads: type: type-add: type-clear: type-not:`,
}

// rgRuns finds the programs ripgrep runs: that of --pre with the path of
// each file it searches, that of --hostname-bin with no words, for the
// name of the host. It runs each as it is, with no shell: the value is one
// word, a path or a name looked up in PATH. --pre= with no value runs none.
func rgRuns(w words, program bool) []run {
	opts, _ := rgOpts.read(w.args)
	rs := optLoose(w, rgOpts, opts, program)
	for _, o := range opts {
		switch {
		case o.value == "":
		case o.name == "pre":
			rs = append(rs, run{text: quoted(o.value) + " " + unknown("{}"), words: []int{o.word}})
		case o.name == "hostname-bin":
			rs = append(rs, run{text: quoted(o.value), words: []int{o.word}})
		}
	}
	return rs
}

// atOpts are the options of at 3.2 and batch: -l, -r and -d list or
// remove jobs and -c prints them, -f reads the commands of the job from a
// file in place of stdin.
var atOpts = getopt{short: "q:f:Mmu:bvlrdhVct:o:"}

// atRuns finds the commands of a job of at and batch, which a shell runs
// later: those of stdin, or of the file of -f, which the line does not
// show.
func atRuns(w words, program bool) []run {
	opts, _ := atOpts.read(w.args)
	rs := optLoose(w, atOpts, opts, program)
	switch {
	case has(opts, "l", "r", "d", "c", "h"):
		return rs
	case has(opts, "f"):
		for _, f := range values(opts, "f") {
			rs = append(rs, run{words: f.words, mark: dynSource})
		}
		return rs
	case len(w.args) == 1 && w.args[0] == "-V":
		// The version alone.
		return rs
	}
	return append(rs, run{stdin: true})
}

// sftpBatch finds the commands sftp reads, of which those of sftpLines
// run a shell here: from the file of -b, or from stdin with -b - or with
// no -b and a host.
func sftpBatch(w words, opts []option, ops []int) []run {
	bs := values(opts, "b")
	if len(bs) == 0 {
		if len(ops) == 0 {
			return nil
		}
		return []run{{stdin: true, lines: sftpLines}}
	}
	var rs []run
	for _, b := range bs {
		switch b.text {
		case "-":
			rs = append(rs, run{words: b.words, stdin: true, lines: sftpLines})
		case "/dev/null":
			rs = append(rs, run{words: b.words})
		default:
			rs = append(rs, run{words: b.words, mark: dynSource})
		}
	}
	return rs
}

// sftpLines finds the code in the commands sftp reads, as its parse_args
// reads a line: after blanks and the prefixes "-" and "@", "!" hands the
// rest of the line to $SHELL -c and alone starts $SHELL, which reads
// stdin; lls hands "ls" and the rest to $SHELL -c.
func sftpLines(text string) (code []string, mark string) {
	const blank = " \t\r\n"
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimLeft(l, blank)
		l = strings.TrimLeft(strings.TrimLeft(l, "-@"), blank)
		if rest, ok := strings.CutPrefix(l, "!"); ok {
			if rest == "" {
				mark = dynStdin
				continue
			}
			code = append(code, rest)
			continue
		}
		first, _, _ := strings.Cut(strings.TrimRight(l, blank), " ")
		first, _, _ = strings.Cut(first, "\t")
		if strings.EqualFold(strings.NewReplacer(`"`, "", "'", "", `\`, "").Replace(first), "lls") && len(l) >= 3 {
			code = append(code, strings.TrimSpace("ls "+strings.TrimLeft(l[3:], blank)))
		}
	}
	return code, mark
}

// fed returns the code a runner reading texts on stdin hands to a shell:
// all of each, or the code r.lines finds in it.
func (p *parser) fed(r run, texts []string) []string {
	if r.lines == nil {
		return texts
	}
	var code []string
	for _, t := range texts {
		c, mark := r.lines(t)
		if mark != "" {
			p.mark(mark)
		}
		code = append(code, c...)
	}
	return code
}

// vimRuns marks the commands of Vim and its kin that run a shell: :!, a
// filter, :r !, :w !, system() and systemlist() in a command of -c, --cmd
// or +, or in the keys and expression they send to another Vim. The rest
// of the language of Vim is not read. A word made at run time that may be
// one of them, the program's, marks the line; a glob is names of files.
func vimRuns(w words, program bool) []run {
	var rs []run
	ex := func(k int, cmd string) {
		switch {
		case !w.static[k]:
			rs = append(rs, run{words: []int{k}})
		case strings.Contains(cmd, "!") || strings.Contains(cmd, "system"):
			rs = append(rs, run{mark: dynComputed})
		}
	}
	for k := 0; k < len(w.args); k++ {
		a := w.args[k]
		if !w.static[k] {
			if c, ok := firstChar(a); program && (w.optionLike(k) || ok && c == '+') {
				rs = append(rs, run{words: []int{k}})
			}
			continue
		}
		switch {
		case a == "--":
			return rs
		case a == "--cmd", a == "--remote-send", a == "--remote-expr":
			if k+1 < len(w.args) {
				k++
				ex(k, w.args[k])
			}
		case strings.HasPrefix(a, "+"):
			ex(k, a[1:])
		case strings.HasPrefix(a, "--"):
		case strings.HasPrefix(a, "-"):
			// -c CMD, -cCMD, or -c after other letters, -ec CMD.
			i := strings.IndexByte(a, 'c')
			switch {
			case i < 0:
			case i+1 < len(a):
				ex(k, a[i+1:])
			case k+1 < len(w.args):
				k++
				ex(k, w.args[k])
			}
		}
	}
	return rs
}
