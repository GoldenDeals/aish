package policy

import (
	"slices"
	"strings"
)

// optRunners are the runners whose code is in the values of their options
// or in words of a syntax of their own, read as their sources read them in
// OpenSSH 10, findutils 4.10, rsync 3.4, GNU tar 1.35 and man-db 2.13: the
// command of find -exec, the local shell commands of ssh -o ProxyCommand=,
// scp -S and rsync -e, the remote ones of ssh -o RemoteCommand= and rsync
// --rsync-path=, those of tar --to-command, -I and --checkpoint-action, the
// pager of man -P. A config file they read (ssh -F) holds any code: it is
// computed. They read their options among their operands, and so a word
// made at run time anywhere may be one of them: program tells that the
// command is the program of the line, and only then is such a word marked
// (see optionLike). As a word of a command no wrapper runs, echo tar "$x",
// it marks nothing.
var optRunners = map[string]func(w words, program bool) []run{
	"find":  findRuns,
	"man":   manRuns,
	"rsync": rsyncRuns,
	"scp":   scpRuns,
	"sftp":  sftpRuns,
	"ssh":   sshRuns,
	"tar":   tarRuns,
}

// The options of the commands above, the long ones with a value at least,
// for the values not to be taken for options. One listed with a value it
// does not take would hide the word after it: none is.
var (
	scpOpts   = getopt{short: "12346ABCTdfOpqRrstvD:F:J:P:S:c:i:l:o:X:"}
	sftpOpts  = getopt{short: "1246AafhNpqrvCc:D:i:l:o:s:S:b:B:F:J:P:R:X:"}
	rsyncOpts = getopt{
		short: "e:B:f:M:T:@:",
		long: `rsh: rsync-path: info: debug: stderr: checksum-choice: cc: block-size: max-delete: max-size: min-size:
			max-alloc: partial-dir: timeout: contimeout: modify-window: temp-dir: compare-dest: copy-dest: link-dest:
			compress-choice: zc: compress-level: zl: skip-compress: filter: exclude: exclude-from: include: include-from:
			files-from: address: port: sockopts: out-format: log-format: log-file: log-file-format: password-file:
			early-input: bwlimit: stop-after: time-limit: stop-at: write-batch: only-write-batch: read-batch: protocol:
			iconv: checksum-seed: chmod: chown: usermap: groupmap: backup-dir: suffix: copy-as: outbuf: config: dparam:
			remote-option:`,
	}
	// tarOpts lists checkpoint, which is a prefix of checkpoint-action:
	// without it --checkpoint would take the next word for an action.
	tarOpts = getopt{
		short: "b:C:f:F:g:H:I:K:L:N:T:V:X:",
		long: `to-command: use-compress-program: checkpoint:: checkpoint-action: info-script: new-volume-script:
			rsh-command: rmt-command: file: directory: exclude: exclude-from: files-from: add-file: transform: xform:
			owner: group: mode: mtime: newer: after-date: newer-mtime: label: format: blocking-factor: record-size:
			tape-length: starting-file: listed-incremental: suffix: backup:: index-file: level: occurrence:: owner-map:
			group-map: quoting-style: quote-chars: no-quote-chars: sort: strip-components: warning: hole-detection:
			xattrs-include: xattrs-exclude: atime-preserve:: exclude-tag: exclude-tag-under: exclude-tag-all:
			pax-option: volno-file: totals:: one-top-level::`,
	}
	manOpts = getopt{
		short: "C:dDfkKlwWcR:L:m:M:S:s:e:iIauP:r:7E:p:tT::H::X::Z?V",
		long: `pager: html:: config-file: debug default warnings:: whatis apropos global-apropos local-file where path
			location where-cat location-cat catman recode: locale: systems: manpath: sections: extension: ignore-case
			match-case regex wildcard names-only all update no-subpages prompt: ascii encoding: no-hyphenation nh
			no-justification nj preprocessor: troff troff-device:: gxditview:: ditroff help usage version`,
	}
)

// optionLike tells whether word k of w, made at run time, may become an
// option of its command: unless the shell keeps its first character as it
// is and that is no "-", or it starts with $HOME or $PWD, which are
// absolute paths. A word that splits with an expansion in it may be any
// words; a glob is the names of files, which no line shows.
func (w words) optionLike(k int) bool {
	s := w.args[k]
	if w.split[k] && strings.ContainsAny(s, "$`") {
		return true
	}
	c, ok := firstChar(s)
	if !ok && w.split[k] && !strings.Contains(s, "{") {
		return false
	}
	return !ok || c == '-'
}

// firstChar finds the first character of what the source form s of a word
// expands to, when the shell keeps it as it is.
func firstChar(s string) (byte, bool) {
	quoted := false
	for strings.HasPrefix(s, `""`) || strings.HasPrefix(s, "''") {
		s = s[2:]
	}
	switch {
	case strings.HasPrefix(s, "'"):
		if len(s) > 1 && s[1] != '\'' {
			return s[1], true
		}
		return 0, false
	case strings.HasPrefix(s, `"`):
		s, quoted = s[1:], true
	}
	for _, v := range []string{"$HOME", "${HOME}", "$PWD", "${PWD}"} {
		if rest, ok := strings.CutPrefix(s, v); ok && (rest == "" || !isNameChar(rest[0]) || v[1] == '{') {
			return '/', true
		}
	}
	switch {
	case s == "", s[0] == '$', s[0] == '`', s[0] == '"':
		return 0, false
	case s[0] == '\\':
		if len(s) > 1 {
			return s[1], true
		}
		return 0, false
	case !quoted && strings.IndexByte("*?[{~", s[0]) >= 0:
		return 0, false
	}
	return s[0], true
}

func isNameChar(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// optLoose returns, for a command that is the program, a run that hangs on
// each word made at run time that may be an option, but the value of one
// read as g reads them or one whose name is fixed (see held): a command
// that reads its options among its operands takes it for one wherever it
// is before a "--".
func optLoose(w words, g getopt, opts []option, program bool) []run {
	if !program {
		return nil
	}
	var lw []int
	isHeld := held(g, opts, w.args)
	for k := range w.args {
		if w.static[k] && w.args[k] == "--" {
			break
		}
		if (!w.static[k] || w.split[k]) && !isHeld(k) && w.optionLike(k) {
			lw = append(lw, k)
		}
	}
	if lw == nil {
		return nil
	}
	return []run{{words: lw}}
}

// sshRuns finds the code of the options of ssh: -o ProxyCommand= and the
// other keywords of sshKeys, -F and -I. Its words made at run time are
// marked by handed.
func sshRuns(w words, _ bool) []run {
	opts, ops := sshOpts.read(w.args)
	if len(ops) > 0 {
		if host := ops[0]; host == 0 || w.args[host-1] != "--" {
			more, _ := sshOpts.read(w.args[host+1:])
			for _, o := range more {
				o.word += host + 1
				opts = append(opts, o)
			}
		}
	}
	var rs []run
	for _, o := range opts {
		switch o.name {
		case "I":
			// A PKCS#11 library, loaded into ssh.
			rs = append(rs, run{words: []int{o.word}, mark: dynRebind})
		default:
			rs = append(rs, sshShared(w, o)...)
		}
	}
	return rs
}

// scpRuns finds the code of scp, as scpCode does.
func scpRuns(w words, program bool) []run {
	opts, _ := scpOpts.read(w.args)
	return scpCode(w, scpOpts, opts, program)
}

// sftpRuns finds the code of sftp, as scpCode does.
func sftpRuns(w words, program bool) []run {
	opts, _ := sftpOpts.read(w.args)
	return scpCode(w, sftpOpts, opts, program)
}

// scpCode finds the code of scp and sftp, which read their options among
// their operands: that of ssh, the program of -S, which they run in place
// of ssh, and the sftp server of -D, which they run here in place of one
// over there; that of sftp -s runs there when it is a path.
func scpCode(w words, g getopt, opts []option, program bool) []run {
	rs := optLoose(w, g, opts, program)
	for _, o := range opts {
		switch o.name {
		case "S":
			rs = append(rs, run{text: quoted(o.value), words: []int{o.word}})
		case "D":
			rs = append(rs, run{text: o.value, words: []int{o.word}})
		case "s":
			if strings.Contains(o.value, "/") {
				rs = append(rs, run{text: o.value, words: []int{o.word}, remote: true})
			}
		default:
			rs = append(rs, sshShared(w, o)...)
		}
	}
	return rs
}

// sshShared finds the code of an option ssh, scp and sftp share: the
// keywords of -o and the config file of -F, which may hold any of them.
func sshShared(w words, o option) []run {
	switch o.name {
	case "o":
		return sshKeyword(w, o)
	case "F":
		switch {
		case !w.static[o.word]:
			return []run{{words: []int{o.word}}}
		case o.value == "none", o.value == "/dev/null":
			return nil
		}
		return []run{{mark: dynComputed}}
	}
	return nil
}

// sshKeys are the keywords of ssh_config whose value is code: a line for a
// shell here (ProxyCommand, LocalCommand, and XAuthLocation, which popen
// runs with arguments; KnownHostsCommand ssh splits itself) or over there
// (RemoteCommand), or a library ssh loads (rebind).
var sshKeys = map[string]run{
	"knownhostscommand":   {},
	"localcommand":        {},
	"pkcs11provider":      {mark: dynRebind},
	"proxycommand":        {},
	"remotecommand":       {remote: true},
	"securitykeyprovider": {mark: dynRebind},
	"xauthlocation":       {},
}

// sshKeyword finds the code of ssh -o KEYWORD=VALUE (or KEYWORD VALUE), as
// ssh reads a line of its config: the keyword in any case, then blanks and
// "=" up to the value. "none" is no command. Of a word made at run time
// the keyword is looked up in its source form, when no expansion is in it.
func sshKeyword(w words, o option) []run {
	key, value, ok := sshSplit(o.value)
	if !w.static[o.word] {
		key, ok = sourceKey(o.value)
	}
	r, code := sshKeys[strings.ToLower(key)]
	switch {
	case !ok:
		return []run{{words: []int{o.word}}}
	case !code:
		return nil
	case !w.static[o.word]:
		return []run{{words: []int{o.word}}}
	case r.mark != "":
		return []run{{words: []int{o.word}, mark: r.mark}}
	case strings.EqualFold(value, "none"), strings.TrimSpace(value) == "":
		return nil
	}
	return []run{{text: value, words: []int{o.word}, remote: r.remote}}
}

// remoteCommand tells whether o is ssh -o RemoteCommand=, but none.
func remoteCommand(o option) bool {
	key, value, ok := sshSplit(o.value)
	return o.name == "o" && ok && strings.EqualFold(key, "remotecommand") && !strings.EqualFold(value, "none") && strings.TrimSpace(value) != ""
}

// sshSplit splits a line of ssh_config into its keyword and value as
// OpenSSH's strdelim does: a quote in the keyword is taken away up to the
// next one, blanks and an "=" end it.
func sshSplit(s string) (key, value string, ok bool) {
	s = strings.TrimLeft(s, " \t")
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			j := strings.IndexByte(s[i+1:], '"')
			if j < 0 {
				return "", "", false
			}
			return s[:i] + s[i+1:i+1+j], strings.TrimLeft(s[i+2+j:], " \t="), true
		case ' ', '\t', '=':
			return s[:i], strings.TrimLeft(s[i:], " \t="), true
		}
	}
	return s, "", true
}

// sourceKey finds the keyword of a KEYWORD=VALUE word made at run time from
// its source form: the text before "=" or a blank, out of its quotes; ok is
// false when an expansion or an escape is in it.
func sourceKey(s string) (string, bool) {
	end := strings.IndexAny(s, "= \t")
	if end < 0 {
		end = len(s)
	}
	head := s[:end]
	if strings.ContainsAny(head, "$`\\") {
		return "", false
	}
	return strings.NewReplacer(`"`, "", "'", "").Replace(head), true
}

// rsyncRuns finds the code of rsync: the remote shell of -e, which it
// splits into words itself, and the program over there of --rsync-path,
// which that shell runs.
func rsyncRuns(w words, program bool) []run {
	opts, _ := rsyncOpts.read(w.args)
	rs := optLoose(w, rsyncOpts, opts, program)
	for _, o := range opts {
		switch o.name {
		case "e", "rsh":
			rs = append(rs, run{text: o.value, words: []int{o.word}})
		case "rsync-path":
			rs = append(rs, run{text: o.value, words: []int{o.word}, remote: true})
		}
	}
	return rs
}

// tarRuns finds the code of tar, which it runs with sh -c: the command of
// --to-command, the compressor of -I, the script of -F and the exec= of
// --checkpoint-action; the program of --rsh-command, and that of
// --rmt-command over there.
func tarRuns(w words, program bool) []run {
	opts := tarRead(w.args)
	rs := optLoose(w, tarOpts, opts, program)
	for _, o := range opts {
		r := run{text: o.value, words: []int{o.word}}
		switch o.name {
		case "to-command", "I", "use-compress-program", "F", "info-script", "new-volume-script", "rsh-command":
		case "rmt-command":
			r.remote = true
		case "checkpoint-action":
			cmd, ok := strings.CutPrefix(o.value, "exec=")
			if !ok {
				continue
			}
			r.text = cmd
		default:
			continue
		}
		rs = append(rs, r)
	}
	return rs
}

// tarRead reads the options of tar as GNU tar does: a first word that is no
// option is letters of options, each that takes a value taking the next
// word in turn, then options among the operands.
func tarRead(args []string) []option {
	if len(args) == 0 || args[0] == "" || args[0][0] == '-' {
		opts, _ := tarOpts.read(args)
		return opts
	}
	var opts []option
	next := 1
	for _, c := range []byte(args[0]) {
		o := option{name: string(c), word: 0}
		if colons(tarOpts.short, c) == 1 && next < len(args) {
			o.value, o.word = args[next], next
			next++
		}
		opts = append(opts, o)
	}
	more, _ := tarOpts.read(args[next:])
	for _, o := range more {
		o.word += next
		opts = append(opts, o)
	}
	return opts
}

// manRuns finds the pager of man -P and the browser of -H, which man runs.
func manRuns(w words, program bool) []run {
	opts, _ := manOpts.read(w.args)
	rs := optLoose(w, manOpts, opts, program)
	for _, o := range opts {
		switch o.name {
		case "P", "pager", "H", "html":
			if o.value != "" {
				rs = append(rs, run{text: o.value, words: []int{o.word}})
			}
		}
	}
	return rs
}

// findExecs are the actions of find that run a command: the words after
// them up to a ";", or a "+" after a word with {} in it.
var findExecs = map[string]bool{"-exec": true, "-execdir": true, "-ok": true, "-okdir": true}

// findValued are the primaries and options of find that take the words
// after them for their values: those words are no primary.
var findValued = map[string]int{
	"-D": 1, "-amin": 1, "-anewer": 1, "-atime": 1, "-cmin": 1, "-cnewer": 1, "-context": 1, "-ctime": 1,
	"-files0-from": 1, "-fls": 1, "-fprint": 1, "-fprint0": 1, "-fprintf": 2, "-fstype": 1, "-gid": 1,
	"-group": 1, "-ilname": 1, "-iname": 1, "-inum": 1, "-ipath": 1, "-iregex": 1, "-iwholename": 1,
	"-links": 1, "-lname": 1, "-maxdepth": 1, "-mindepth": 1, "-mmin": 1, "-mtime": 1, "-name": 1,
	"-newer": 1, "-path": 1, "-perm": 1, "-printf": 1, "-regex": 1, "-regextype": 1, "-samefile": 1,
	"-size": 1, "-type": 1, "-uid": 1, "-used": 1, "-user": 1, "-wholename": 1, "-xtype": 1,
}

// findTakes tells how many words a word of find takes for its values;
// -newerXY is -newer with two letters.
func findTakes(a string) int {
	if n, ok := findValued[a]; ok {
		return n
	}
	if len(a) == len("-newerXY") && strings.HasPrefix(a, "-newer") {
		return 1
	}
	return 0
}

// findRuns finds the commands of find: those of -exec and its kin, each
// word with {} in it made at run time, as find puts a path there; those of
// -execdir and -okdir run in the directory of the path. As the program, a
// word made at run time that may be a primary or end a command marks the
// line when it may start a command the words do not show (see findWild).
func findRuns(w words, program bool) []run {
	var rs []run
	starts := map[int]bool{}
	for k := 0; k < len(w.args); k++ {
		a := w.args[k]
		if !w.static[k] {
			continue
		}
		if n := findTakes(a); n > 0 {
			k += n
			continue
		}
		if !findExecs[a] {
			continue
		}
		starts[k+1] = true
		cmd := findCommand(w, k+1)
		if len(cmd) > 0 {
			v := words{w.args, slices.Clone(w.static), w.split}
			for _, j := range cmd {
				if strings.Contains(w.args[j], "{}") {
					v.static[j] = false
				}
			}
			r := v.argv(cmd)
			r.moved = a == "-execdir" || a == "-okdir"
			rs = append(rs, r)
		}
		k += len(cmd) + 1
	}
	if program {
		if mark := findWild(w, starts); mark != "" {
			rs = append(rs, run{mark: mark})
		}
	}
	return rs
}

// findCommand returns the indexes of the words of a command of find from
// word i, up to the word that ends it or the end.
func findCommand(w words, i int) []int {
	var cmd []int
	for k := i; k < len(w.args); k++ {
		switch a := w.args[k]; {
		case w.static[k] && a == ";":
			return cmd
		case w.static[k] && a == "+" && len(cmd) > 0 && strings.Contains(w.args[k-1], "{}"):
			return cmd
		}
		cmd = append(cmd, k)
	}
	return cmd
}

// findState is where find is in its words: in its expression, taking n
// values, at the program of a command or in a command from word start,
// whose program may be one (prog) and whose last word has {} (braces).
type findState struct {
	kind   byte // 'e', 'v', 's', 'c'
	n      int
	start  int
	prog   bool
	braces bool
}

// findWild tells whether a word of find made at run time may make it run a
// command its words do not show: as a primary that starts one ("$a" sudo ls
// \;), takes a value, or as the ";" that ends one (-exec echo "$x" -exec
// sudo ls \;). Each such word is followed as any of these and as a plain
// word at once; a command that may run and none of starts is computed, and
// so is a word that may be several.
func findWild(w words, starts map[int]bool) string {
	states := map[findState]bool{{kind: 'e'}: true}
	for k, a := range w.args {
		static := w.static[k]
		c, known := firstChar(a)
		loose := !static && (w.optionLike(k) || known && strings.IndexByte(";+()!,", c) >= 0)
		switch {
		case w.split[k] && loose:
			return dynComputed
		case !static && !loose:
			// Made at run time of a plain first character, it is no
			// primary and no end of a command.
			static, a = true, "x"
		}
		next := map[findState]bool{}
		for s := range states {
			for _, t := range findStep(s, k, a, static, w.args[k]) {
				if t.kind == 'd' {
					// A command that ends: one of starts runs as the
					// words show it.
					if t.prog && !starts[t.start] {
						return dynComputed
					}
					t = findState{kind: 'e'}
				}
				next[t] = true
			}
		}
		states = next
	}
	return ""
}

// findStep returns the states find may be in after word k, a, from s: a
// word made at run time (not static) may be any word. A state of kind 'd'
// is a command that ends there.
func findStep(s findState, k int, a string, static bool, src string) []findState {
	switch s.kind {
	case 'v':
		if s.n > 1 {
			return []findState{{kind: 'v', n: s.n - 1}}
		}
		return []findState{{kind: 'e'}}
	case 's':
		prog := !static || !strings.HasPrefix(a, "-") && !slices.Contains([]string{";", "+", "(", ")", "!", ","}, a)
		if static && a == ";" {
			// No command: find fails.
			return nil
		}
		return []findState{{kind: 'c', start: k, prog: prog, braces: !static || strings.Contains(src, "{}")}}
	case 'c':
		end := findState{kind: 'd', start: s.start, prog: s.prog}
		in := findState{kind: 'c', start: s.start, prog: s.prog, braces: !static || strings.Contains(src, "{}")}
		switch {
		case !static:
			return []findState{in, end}
		case a == ";":
			return []findState{end}
		case a == "+" && s.braces:
			return []findState{in, end}
		}
		return []findState{in}
	}
	if !static {
		return []findState{{kind: 'e'}, {kind: 's'}, {kind: 'v', n: 1}, {kind: 'v', n: 2}}
	}
	switch {
	case findExecs[a]:
		return []findState{{kind: 's'}}
	case findTakes(a) > 0:
		return []findState{{kind: 'v', n: findTakes(a)}}
	}
	return []findState{{kind: 'e'}}
}
