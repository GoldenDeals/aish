package policy

import (
	"slices"
	"strings"
)

// parallelShort are the options of GNU parallel 20260822 of one letter as
// getopt reads them; "::" is a value Getopt::Long takes from the rest of the
// word or else the next word, unless that looks like an option.
const parallelShort = "+D:mXvkgu0qI:U:S:MB:W:oTH:J:P:j:d:s:a:ri::E:e::n:N:C:hL:l::ptVxY"

// parallelLetters are the long names of the options of one letter that
// parallelRuns looks at.
var parallelLetters = map[byte]string{'a': "arg-file", 'i': "replace", 'q': "quote", 'S': "sshlogin"}

// parallelLong are the long options of GNU parallel 20260822 by each of
// their names: the first name and whether it takes a value, 2 for one it
// takes as an optional value of one letter does.
var parallelLong = func() map[string]parallelOpt {
	m := map[string]parallelOpt{}
	for _, f := range strings.Fields(`debug= xargs sql= sql-master|sqlmaster= sql-worker|sqlworker=
		sql-and-worker|sqlandworker= joblog|jl= results|result|res= resume resume-failed|resumefailed
		retry-failed|retryfailed silent keep-order|keeporder no-keep-order|nokeeporder|nok|no-k group
		ungroup latest-line|latestline|ll line-buffer|line-buffered|linebuffer|linebuffered|lb tmux
		tmux-pane|tmuxpane null quote parens= rpl= plus extensionreplace|er= basenamereplace|bnr=
		dirnamereplace|dnr= basenameextensionreplace|bner= seqreplace= slotreplace= delay=
		ssh-delay|sshdelay= load= noswap max-line-length-allowed|maxlinelengthallowed
		number-of-cpus|numberofcpus number-of-sockets|numberofsockets number-of-cores|numberofcores
		number-of-threads|numberofthreads use-sockets-instead-of-threads|usesocketsinsteadofthreads
		use-cores-instead-of-threads|usecoresinsteadofthreads use-cpus-instead-of-cores|usecpusinsteadofcores
		shell-quote|shellquote|shell_quote nice= tag tag-string|tagstring= ctag ctag-string|ctagstring=
		color|colour color-failed|colour-failed|colorfailed|colourfailed|color-fail|colour-fail|colorfail|colourfail|cf
		onall nonall filter-hosts|filterhosts|filter-host sshlogin= sshloginfile|slf= controlmaster ssh=
		transfer-file|transferfile|transfer-files|transferfiles|tf= return= trc= transfer cleanup
		basefile|bf= template|tmpl= ctrl-c|ctrlc no-ctrl-c|no-ctrlc|noctrlc work-dir|workdir|wd=
		rsync-opts|rsyncopts= tmpdir|tempdir= use-compress-program|compress-program|usecompressprogram|compressprogram=
		use-decompress-program|decompress-program|usedecompressprogram|decompressprogram= compress
		open-tty tty dry-run|dryrun|dr progress eta bar total-jobs|totaljobs|total= shuf milestone|ms=
		arg-sep|argsep= arg-file-sep|argfilesep= trim= env= recordenv|record-env session plain
		profile= tollef gnu link|xapply linkinputsource|xapplyinputsource= bibtex|citation
		will-cite|willcite|nn|nonotice|no-notice halt-on-error|haltonerror|halt= limit= memfree=
		memsuspend= retries= timeout= term-seq|termseq= max-procs|maxprocs|jobs= delimiter=
		max-chars|maxchars= arg-file|argfile= no-run-if-empty|norunifempty replace: eof:
		process-slot-var|processslotvar= max-args|maxargs= max-replace-args|maxreplaceargs=
		col-sep|colsep= match= csv help max-lines|maxlines: interactive verbose version
		min-version|minversion= show-limits|showlimits exit semaphore semaphore-timeout|semaphoretimeout|st=
		semaphore-name|semaphorename|id= fg bg wait shebang|hashbang _pipe-means-argfiles
		skip-first-line|skipfirstline unsafe _bug pipe|spreadstdin round-robin|roundrobin|round recstart=
		recend= regexp|regex remove-rec-sep|removerecsep|rrs output-as-files|outputasfiles|files
		output-as-files0|outputasfiles0|files0 block-size|blocksize|block= block-timeout|blocktimeout|bt=
		header= cat fifo pipe-part|pipepart tee shard= bin= group-by|groupby=
		hgrp|hostgrp|hostgroup|hostgroups embed filter= combineexec|combine-exec|combineexecutable|combine-executable=
		fast _parset= _pipe_block= _buf_start= _buf_growth= _buf_cap= _no_blocksize_warning
		shell-completion|shellcompletion=`) {
		takes := 0
		switch {
		case strings.HasSuffix(f, "="):
			takes = 1
		case strings.HasSuffix(f, ":"):
			takes = 2
		}
		names := strings.Split(strings.TrimRight(f, "=:"), "|")
		for _, n := range names {
			m[n] = parallelOpt{names[0], takes}
		}
	}
	return m
}()

// parallelOpt is a long option of parallel: its first name and what value
// it takes.
type parallelOpt struct {
	name  string
	takes int
}

// parallelOption finds a long option of parallel by a name in any case, or
// by a prefix of the names of one option only, as Getopt::Long does.
func parallelOption(name string) (parallelOpt, bool) {
	name = strings.ToLower(name)
	if o, ok := parallelLong[name]; ok {
		return o, true
	}
	var found parallelOpt
	n := 0
	for k, o := range parallelLong {
		if strings.HasPrefix(k, name) && (n == 0 || o.name != found.name) {
			found = o
			n++
		}
	}
	return found, n == 1
}

// parallelRead reads the options of parallel as Getopt::Long does with
// bundling and require_order, up to the first operand or "--": letters
// bundled in a word with a value in the rest of it or the next word, long
// options by a prefix of their name with a value after "=" or in the next
// word. An optional value is the next word unless that looks like an
// option. The options are named by their first long name.
func parallelRead(args []string) (opts []option, cmd int) {
	short := strings.TrimPrefix(parallelShort, "+")
	next := func(i int, optional bool) bool {
		return i+1 < len(args) && (!optional || args[i+1] != "--" && (len(args[i+1]) < 2 || args[i+1][0] != '-'))
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return opts, i + 1
		case len(a) < 2 || a[0] != '-':
			return opts, i
		case strings.HasPrefix(a, "--"):
			name, v, eq := strings.Cut(a[2:], "=")
			o := option{name: strings.ToLower(name), value: v, word: i}
			if p, ok := parallelOption(name); ok {
				o.name = p.name
				if !eq && p.takes > 0 && next(i, p.takes == 2) {
					i++
					o.value, o.word = args[i], i
				}
			}
			opts = append(opts, o)
		default:
			for j := 1; j < len(a); j++ {
				o := option{name: a[j : j+1], word: i}
				if l, ok := parallelLetters[a[j]]; ok {
					o.name = l
				}
				if takes := colons(short, a[j]); takes > 0 {
					o.value = a[j+1:]
					if o.value == "" && next(i, takes == 2) {
						i++
						o.value, o.word = args[i], i
					}
					j = len(a)
				}
				opts = append(opts, o)
			}
		}
	}
	return opts, len(args)
}

// maxInputs is how many commands parallel with no command of its own may
// make of the values of several sources before they are marked instead.
const maxInputs = 64

// parallelRuns finds the code of GNU parallel and of sem, which is parallel
// --semaphore: its command, run by a shell with the values of its inputs in
// it, or with no command the inputs themselves; the code of --ssh, --limit
// and the programs of compression, and the Perl of {= =}, --rpl and
// --filter, which is marked.
func parallelRuns(w words, sem bool) []run {
	opts, cmd := parallelRead(w.args)
	var rs []run
	if lw := w.loose(cmd, held(getopt{short: parallelShort}, opts, w.args)); len(lw) > 0 {
		rs = append(rs, run{words: lw})
	}
	left, argSep, fileSep := "{=", ":::", "::::"
	for _, o := range opts {
		switch o.name {
		case "rpl", "filter":
			rs = append(rs, run{mark: dynComputed})
		case "shard", "bin", "group-by":
			// A column, or a column and Perl.
			if strings.ContainsAny(o.value, " \t") {
				rs = append(rs, run{mark: dynComputed})
			}
		case "ssh", "limit", "use-compress-program", "use-decompress-program":
			rs = append(rs, run{text: o.value, words: []int{o.word}})
		case "parens":
			left = o.value[:len(o.value)/2]
		case "arg-sep":
			argSep = o.value
		case "arg-file-sep":
			fileSep = o.value
		case "semaphore":
			sem = true
		}
	}
	if left != "" && slices.ContainsFunc(w.args[:cmd], func(a string) bool { return strings.Contains(a, left) }) {
		rs = append(rs, run{mark: dynComputed})
	}
	sep := len(w.args)
	isSep := func(k int) (file, ok bool) {
		a := strings.TrimSuffix(w.args[k], "+")
		return a == fileSep, w.static[k] && (a == argSep || a == fileSep)
	}
	for k := cmd; k < len(w.args); k++ {
		if _, ok := isSep(k); ok {
			sep = k
			break
		}
	}
	var jobs []run
	switch {
	case cmd < sep:
		jobs = parallelTemplate(w, opts, indexes(cmd, sep), left)
	case sem, has(opts, parallelNoRun...):
	default:
		jobs = parallelInputs(w, sep, has(opts, "arg-file"), isSep)
	}
	if has(opts, "work-dir", "sshlogin", "sshloginfile") {
		// In a directory of its own, here or on another machine.
		jobs = elsewhere(jobs)
	}
	return append(rs, jobs...)
}

// parallelNoRun are the options with which parallel runs no command of its
// inputs: it prints what it is asked and exits, or what it would run.
var parallelNoRun = []string{
	"bibtex", "dry-run", "embed", "help", "max-line-length-allowed", "number-of-cores",
	"number-of-cpus", "number-of-sockets", "number-of-threads", "record-env", "session",
	"shell-completion", "shell-quote", "show-limits", "version", "h", "V",
}

// parallelInputs finds the commands of parallel with no command of its own:
// its inputs, a command of each combination of the values of its sources.
// Those of ::: are in the line, those of stdin are read as a shell reads
// them; a file, of :::: or -a, is no more marked than a script bash runs.
func parallelInputs(w words, sep int, files bool, isSep func(int) (bool, bool)) []run {
	var groups [][]int
	cur := -1
	for k := sep; k < len(w.args); k++ {
		if file, ok := isSep(k); ok {
			cur = -1
			if file {
				files = true
			} else {
				groups = append(groups, nil)
				cur = len(groups) - 1
			}
			continue
		}
		if cur >= 0 {
			groups[cur] = append(groups[cur], k)
		}
	}
	if len(groups) == 0 {
		if files {
			return nil
		}
		return []run{{stdin: true}}
	}
	combos := [][]int{nil}
	for _, g := range groups {
		var more [][]int
		for _, c := range combos {
			for _, k := range g {
				more = append(more, append(slices.Clone(c), k))
			}
		}
		if combos = more; len(combos) > maxInputs {
			break
		}
	}
	var rs []run
	if files || len(combos) > maxInputs {
		// What a command is made of is not all in the line: each value
		// is read alone.
		if !files {
			rs = append(rs, run{mark: dynComputed})
		}
		combos = nil
		for _, g := range groups {
			for _, k := range g {
				combos = append(combos, []int{k})
			}
		}
	}
	for _, c := range combos {
		parts := make([]string, len(c))
		for n, k := range c {
			parts[n] = w.args[k]
		}
		rs = append(rs, run{text: strings.Join(parts, " "), words: c})
	}
	return rs
}

// parallelTemplate finds the code of the command of parallel, its words
// idx: joined with spaces into a line for the shell or, with -q, each a word
// as it is. A replacement string such as {} or {.} stands for a value that
// parallel puts in quotes of its own, one word made at run time out of
// quotes; in quotes or after a backslash the value ends the quotes of the
// line, which is marked. So is the Perl of {= =}.
func parallelTemplate(w words, opts []option, idx []int, left string) []run {
	rp := replacements(opts)
	parts := make([]string, len(idx))
	for n, k := range idx {
		parts[n] = w.args[k]
	}
	line := strings.Join(parts, " ")
	var r run
	if has(opts, "quote") {
		var b strings.Builder
		for n, k := range idx {
			if n > 0 {
				b.WriteByte(' ')
			}
			switch a := w.args[k]; {
			case w.split[k]:
				r.words = append(r.words, k)
				b.WriteString(unknown(a))
			case !w.static[k], rp.in(a):
				b.WriteString(unknown(a))
			default:
				b.WriteString(quoted(a))
			}
		}
		r.text = b.String()
	} else {
		text, ok := rp.template(line)
		r = run{text: text, words: idx}
		if !ok {
			r.mark = dynComputed
		}
	}
	if left != "" && strings.Contains(line, left) {
		r.mark = dynComputed
	}
	return []run{r}
}

// rpls are the replacement strings of parallel: those its options name,
// and in braces {} and its kin, or with --plus any word in braces.
type rpls struct {
	custom []string
	plus   bool
}

func replacements(opts []option) rpls {
	var r rpls
	for _, o := range opts {
		switch o.name {
		case "I", "replace", "extensionreplace", "basenamereplace", "dirnamereplace", "basenameextensionreplace", "seqreplace", "slotreplace":
			if o.value != "" {
				r.custom = append(r.custom, o.value)
			}
		case "plus":
			r.plus = true
		}
	}
	slices.SortFunc(r.custom, func(a, b string) int { return len(b) - len(a) })
	return r
}

// at returns the length of the replacement string at s[i:], 0 for none.
func (r rpls) at(s string, i int) int {
	for _, c := range r.custom {
		if strings.HasPrefix(s[i:], c) {
			return len(c)
		}
	}
	if s[i] != '{' {
		return 0
	}
	end := strings.IndexByte(s[i+1:], '}')
	if end < 0 {
		return 0
	}
	body := s[i+1 : i+1+end]
	if r.plus && !strings.ContainsAny(body, "{ \t\n") || rplBody(body) {
		return end + 2
	}
	return 0
}

// in tells whether s holds a replacement string.
func (r rpls) in(s string) bool {
	for i := range len(s) {
		if r.at(s, i) > 0 {
			return true
		}
	}
	return false
}

// rplBody tells whether {body} is a replacement string of parallel without
// --plus: {}, {.}, {/}, {//}, {/.}, {#}, {%} and those of a position, such
// as {2} and {-1/.}.
func rplBody(body string) bool {
	switch strings.TrimLeft(strings.TrimPrefix(body, "-"), "0123456789") {
	case "", ".", "/", "//", "/.", "#", "%":
		return true
	}
	return false
}

// template makes a line of parallel one of bash in which a replacement
// string out of quotes, in the line or in a $(…) of it, is a word made at
// run time. ok is false for one in quotes, in `…` or after a backslash.
func (r rpls) template(s string) (string, bool) {
	var b strings.Builder
	ok := true
	// The quotes the parser is in: ' and " are theirs, a is $'…', ( is
	// $(…) and ` is `…`; 0 is the line itself.
	stack := []byte{0}
	esc := false
	for i := 0; i < len(s); {
		top := stack[len(stack)-1]
		if n := r.at(s, i); n > 0 {
			if esc || top != 0 && top != '(' {
				ok = false
				b.WriteString(s[i : i+n])
			} else {
				b.WriteString(unknown(s[i : i+n]))
			}
			esc = false
			i += n
			continue
		}
		c := s[i]
		b.WriteByte(c)
		i++
		switch {
		case esc:
			esc = false
		case top == '\'':
			if c == '\'' {
				stack = stack[:len(stack)-1]
			}
		case c == '\\':
			esc = true
		case top == 'a':
			if c == '\'' {
				stack = stack[:len(stack)-1]
			}
		case c == '`':
			if top == '`' {
				stack = stack[:len(stack)-1]
			} else {
				stack = append(stack, '`')
			}
		case c == '$' && i < len(s) && s[i] == '(':
			b.WriteByte('(')
			i++
			stack = append(stack, '(')
		case top == '"':
			if c == '"' {
				stack = stack[:len(stack)-1]
			}
		case c == '$' && i < len(s) && s[i] == '\'':
			b.WriteByte('\'')
			i++
			stack = append(stack, 'a')
		case c == '\'', c == '"':
			stack = append(stack, c)
		case top == '(' && c == '(':
			stack = append(stack, '(')
		case top == '(' && c == ')':
			stack = stack[:len(stack)-1]
		}
	}
	return b.String(), ok
}
