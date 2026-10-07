package policy

import (
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The options of tmux 3.7 before its command.
var tmuxOpts = getopt{short: "+2CDdhlNqUuVvc:f:L:S:T:"}

// tmuxRuns finds the code of tmux: that of tmux -c, the shell commands of
// its commands, the keys send-keys types and the commands of #(…), which
// run where tmux expands a word as a format.
func tmuxRuns(w words) []run {
	opts, ops := tmuxOpts.read(w.args)
	start := len(w.args)
	if len(ops) > 0 {
		start = ops[0]
	}
	rs := formats(w)
	if lw := w.loose(start, held(tmuxOpts, opts, w.args)); len(lw) > 0 {
		rs = append(rs, run{words: lw})
	}
	for _, c := range values(opts, "c") {
		rs = append(rs, run{text: c.text, words: c.words})
	}
	if has(opts, "C") {
		// In control mode the client reads commands of tmux from stdin.
		rs = append(rs, run{mark: dynStdin})
	}
	for _, f := range values(opts, "f") {
		// A config file holds commands of tmux, as source-file reads them.
		if f.text != "/dev/null" {
			rs = append(rs, run{words: f.words, mark: dynSource})
		}
	}
	for _, c := range tmuxSplit(w, start) {
		rs = append(rs, tmuxCommand(c)...)
	}
	return elsewhere(rs)
}

// tmuxSplit splits the words of tmux from start into its commands, as tmux
// reads them from its arguments: a word ";" ends a command, and so does a
// word ending in a ";" without a backslash before it, which is no part of
// the word; "\;" at the end of a word is a ";" of it.
func tmuxSplit(w words, start int) []sub {
	var subs []sub
	var cur sub
	for k := start; k < len(w.args); k++ {
		a, end := w.args[k], false
		if s, ok := strings.CutSuffix(a, ";"); ok && w.static[k] {
			if t, esc := strings.CutSuffix(s, `\`); esc {
				a = t + ";"
			} else {
				a, end = s, true
			}
		}
		if !end || a != "" {
			cur.w.args = append(cur.w.args, a)
			cur.w.static = append(cur.w.static, w.static[k])
			cur.w.split = append(cur.w.split, w.split[k])
			cur.idx = append(cur.idx, k)
		}
		if end && len(cur.idx) > 0 {
			subs = append(subs, cur)
			cur = sub{}
		}
	}
	if len(cur.idx) > 0 {
		subs = append(subs, cur)
	}
	return subs
}

// tmuxCmd is how a command of tmux reads its words after its name, and
// what of them it runs, given its options and its operands.
type tmuxCmd struct {
	alias string
	opts  getopt
	runs  func(w words, opts []option, ops []int) []run
}

// tmuxCmds are the commands of tmux 3.7 that run code: a shell command,
// keys typed at a pane, or commands of tmux kept in a string, which this
// parser does not read and marks. bind-key runs the command after its key
// later, which is read as one of tmux.
var tmuxCmds = map[string]tmuxCmd{
	"bind-key":       {"bind", getopt{short: "+nrN:T:"}, nil},
	"choose-buffer":  {"", getopt{short: "+NZrF:f:K:O:t:"}, tmuxKept(0)},
	"choose-client":  {"", getopt{short: "+NZrF:f:K:O:t:"}, tmuxKept(0)},
	"choose-tree":    {"", getopt{short: "+GNrswZF:f:K:O:t:"}, tmuxKept(0)},
	"command-prompt": {"", getopt{short: "+1bCeFiklNI:p:t:T:"}, tmuxKept(0)},
	"confirm-before": {"confirm", getopt{short: "+byc:p:t:"}, tmuxKept(0)},
	"detach-client":  {"detach", getopt{short: "+aPE:s:t:"}, tmuxDetach},
	"display-menu":   {"menu", getopt{short: "+OMb:c:C:H:s:S:t:T:x:y:"}, tmuxKept(2)},
	"display-panes":  {"displayp", getopt{short: "+bNd:t:"}, tmuxKept(0)},
	"display-popup":  {"popup", getopt{short: "+BCEkNb:c:d:e:h:s:S:t:T:w:x:y:"}, tmuxWindow},
	"if-shell":       {"if", getopt{short: "+bFt:"}, tmuxIf},
	"new-pane":       {"newp", getopt{short: "+bdefhIkPvZc:e:F:l:m:p:R:s:S:t:"}, tmuxWindow},
	"new-session":    {"new", getopt{short: "+AdDEPXc:e:f:F:n:s:t:x:y:"}, tmuxWindow},
	"new-window":     {"neww", getopt{short: "+abdkPSc:e:F:n:t:"}, tmuxWindow},
	"paste-buffer":   {"pasteb", getopt{short: "+dprSb:s:t:"}, tmuxPaste},
	"pipe-pane":      {"pipep", getopt{short: "+IOot:"}, tmuxShell},
	"respawn-pane":   {"respawnp", getopt{short: "+kc:e:t:"}, tmuxWindow},
	"respawn-window": {"respawnw", getopt{short: "+kc:e:t:"}, tmuxWindow},
	"run-shell":      {"run", getopt{short: "+bCEc:d:t:"}, tmuxRun},
	"send-keys":      {"send", getopt{short: "+FHKlMRXc:N:t:"}, tmuxKeys},
	"set-hook":       {"", getopt{short: "+agpRuwt:"}, tmuxKept(1)},
	"source-file":    {"source", getopt{short: "+Fnqvt:"}, tmuxSource},
	"split-window":   {"splitw", getopt{short: "+bdefhIkPvZc:e:F:l:m:p:R:s:S:t:"}, tmuxWindow},

	// They set what new windows run, as set-hook does.
	"set-environment":   {"setenv", getopt{short: "+Fhgrt:u"}, tmuxSetenv},
	"set-option":        {"set", getopt{short: "+aFgopqst:uUw"}, tmuxSet},
	"set-window-option": {"setw", getopt{short: "+aFgoqt:u"}, tmuxSet},
}

// tmuxNames are the names and aliases of all the commands of tmux 3.7: one
// of them is the command it names, not a prefix of another.
var tmuxNames = strings.Fields(`attach-session attach bind-key bind break-pane breakp
	capture-pane capturep choose-buffer choose-client choose-tree clear-history clearhist
	clear-prompt-history clearphist clock-mode command-prompt confirm-before confirm
	copy-mode customize-mode delete-buffer deleteb detach-client detach display-menu menu
	display-message display display-popup popup display-panes displayp find-window findw
	has-session has if-shell if join-pane joinp kill-pane killp kill-server kill-session
	kill-window killw last-pane lastp last-window last link-window linkw list-buffers lsb
	list-clients lsc list-commands lscm list-keys lsk list-panes lsp list-sessions ls
	list-windows lsw load-buffer loadb lock-client lockc lock-server lock lock-session
	locks move-pane movep move-window movew new-pane newp new-session new new-window neww
	next-layout nextl next-window next paste-buffer pasteb pipe-pane pipep previous-layout
	prevl previous-window prev refresh-client refresh rename-session rename rename-window
	renamew resize-pane resizep resize-window resizew respawn-pane respawnp respawn-window
	respawnw rotate-window rotatew run-shell run save-buffer saveb select-layout selectl
	select-pane selectp select-window selectw send-keys send send-prefix server-access
	set-buffer setb set-environment setenv set-hook set-option set set-window-option setw
	show-buffer showb show-environment showenv show-hooks show-messages showmsgs
	show-options show show-prompt-history showphist show-window-options showw source-file
	source split-window splitw start-server start suspend-client suspendc swap-pane swapp
	swap-window swapw switch-client switchc unbind-key unbind unlink-window unlinkw
	wait-for wait`)

// tmuxFind finds the commands of tmuxCmds a name runs, as tmux finds a
// command: by its name or alias, else by a prefix of its name. A prefix of
// several names runs none of them; each one of tmuxCmds is taken all the
// same, as other versions of tmux have other commands.
func tmuxFind(name string) []string {
	if name == "" {
		return nil
	}
	if slices.Contains(tmuxNames, name) {
		for full, c := range tmuxCmds {
			if full == name || c.alias == name {
				return []string{full}
			}
		}
		return nil
	}
	var found []string
	for full := range tmuxCmds {
		if strings.HasPrefix(full, name) {
			found = append(found, full)
		}
	}
	slices.Sort(found)
	return found
}

// tmuxCommand finds the code of a command of tmux, its name first.
func tmuxCommand(c sub) []run {
	if !c.w.static[0] {
		// It may be any command.
		return c.at([]run{{words: []int{0}}})
	}
	args := c.from(1)
	var rs []run
	for _, name := range tmuxFind(c.w.args[0]) {
		t := tmuxCmds[name]
		opts, ops := t.opts.read(args.w.args)
		first := len(args.w.args)
		if len(ops) > 0 {
			first = ops[0]
		}
		if lw := args.w.loose(first, held(t.opts, opts, args.w.args)); len(lw) > 0 {
			rs = append(rs, args.at([]run{{words: lw}})...)
		}
		switch {
		case name == "bind-key":
			if len(ops) > 1 {
				rs = append(rs, tmuxCommand(args.from(ops[1]))...)
			}
		default:
			rs = append(rs, args.at(t.runs(args.w, opts, ops))...)
		}
	}
	return rs
}

// tmuxWindow finds the shell command of a new window, pane or popup: one
// operand is a line for the shell, more run as they are; and the variables
// of -e, which it puts in their environment.
func tmuxWindow(w words, opts []option, ops []int) []run {
	rs := tmuxEnv(w, values(opts, "e"))
	switch len(ops) {
	case 0:
		return rs
	case 1:
		return append(rs, run{text: w.args[ops[0]], words: ops})
	}
	return append(rs, w.argv(ops))
}

// tmuxEnv finds the NAME=VALUE of -e: of a word made at run time the name
// is read in its source form, and may be any when made at run time too.
func tmuxEnv(w words, vs []piece) []run {
	var rs []run
	for _, v := range vs {
		if w.static[v.words[0]] {
			rs = append(rs, run{env: v.text})
			continue
		}
		name, ok := sourceKey(v.text)
		if !ok || !strings.Contains(v.text, "=") {
			rs = append(rs, run{words: v.words})
			continue
		}
		rs = append(rs, run{env: name + "=", envDyn: true})
	}
	return rs
}

// tmuxCodeOpts are the options of tmux whose value is code new windows,
// clients or copy mode run later: a line for the shell, the program of
// default-shell, or commands of tmux this parser does not read.
var tmuxCodeOpts = map[string]func(value string) run{
	"command-alias":          tmuxMarked,
	"copy-command":           tmuxLine,
	"default-client-command": tmuxMarked,
	"default-command":        tmuxLine,
	"default-shell":          func(v string) run { return run{text: quoted(v)} },
	"editor":                 tmuxLine,
	"lock-command":           tmuxLine,
}

func tmuxLine(v string) run { return run{text: v} }
func tmuxMarked(string) run { return run{mark: dynComputed} }

// tmuxSet finds the code of set-option: an option of tmuxCodeOpts named by
// its name or, as tmux finds one, a prefix of it, an index [N] left out.
// Its value runs later, in another window: rebind. -F expands it as a
// format first, -a appends it to a value the line does not show.
func tmuxSet(w words, opts []option, ops []int) []run {
	if len(ops) < 2 || has(opts, "u", "U") {
		return nil
	}
	name, val := ops[0], ops[1]
	if !w.static[name] {
		return []run{{words: []int{name}}}
	}
	opt, _, _ := strings.Cut(w.args[name], "[")
	var rs []run
	for _, full := range slices.Sorted(maps.Keys(tmuxCodeOpts)) {
		if opt == "" || !strings.HasPrefix(full, opt) {
			continue
		}
		r := tmuxCodeOpts[full](w.args[val])
		r.words = []int{val}
		if has(opts, "F") && formatted(w.args[val]) || has(opts, "a") {
			r.mark = dynComputed
		}
		rs = append(rs, r, run{mark: dynRebind})
	}
	return rs
}

// tmuxSetenv finds the variable set-environment puts in the environment of
// new windows; one of -h is hidden from them, -F expands a format first.
func tmuxSetenv(w words, opts []option, ops []int) []run {
	if len(ops) < 2 || has(opts, "u", "r", "h") {
		return nil
	}
	if has(opts, "F") && formatted(w.args[ops[1]]) {
		return []run{{mark: dynComputed}}
	}
	if !w.static[ops[0]] {
		return []run{{words: ops[:1]}}
	}
	return []run{{env: w.args[ops[0]] + "=" + w.args[ops[1]], envDyn: !w.static[ops[1]]}}
}

// tmuxShell finds the shell command of pipe-pane.
func tmuxShell(w words, opts []option, ops []int) []run {
	if len(ops) == 0 {
		return nil
	}
	return []run{formatShell(w, ops[0])}
}

// tmuxRun finds the shell command of run-shell; with -C it is a command of
// tmux.
func tmuxRun(w words, opts []option, ops []int) []run {
	switch {
	case len(ops) == 0:
		return nil
	case has(opts, "C"):
		return []run{{mark: dynComputed}}
	}
	return []run{formatShell(w, ops[0])}
}

// tmuxIf finds the shell command of if-shell, which -F makes a format
// only, and marks the commands of tmux it runs after.
func tmuxIf(w words, opts []option, ops []int) []run {
	var rs []run
	if len(ops) > 0 && !has(opts, "F") {
		rs = append(rs, formatShell(w, ops[0]))
	}
	if len(ops) > 1 {
		rs = append(rs, run{mark: dynComputed})
	}
	return rs
}

// tmuxKept marks the commands of tmux a command keeps in its operands from
// the one at from on, to run when a key is pressed or a choice made.
func tmuxKept(from int) func(words, []option, []int) []run {
	return func(w words, opts []option, ops []int) []run {
		if len(ops) > from {
			return []run{{mark: dynComputed}}
		}
		return nil
	}
}

// tmuxDetach finds the shell command of detach-client -E.
func tmuxDetach(w words, opts []option, ops []int) []run {
	var rs []run
	for _, v := range values(opts, "E") {
		rs = append(rs, run{text: v.text, words: v.words})
	}
	return rs
}

// tmuxSource marks the commands source-file reads from files.
func tmuxSource(w words, opts []option, ops []int) []run {
	if len(ops) == 0 {
		return nil
	}
	return []run{{mark: dynSource}}
}

// tmuxPaste marks the text paste-buffer types at a pane: it is in a buffer,
// not in the line.
func tmuxPaste(w words, opts []option, ops []int) []run {
	return []run{{mark: dynComputed}}
}

// tmuxKeys finds what send-keys types at a pane, as the lines of a shell,
// and the shell command of the copy-pipe commands of copy mode (-X).
func tmuxKeys(w words, opts []option, ops []int) []run {
	switch {
	case len(ops) == 0, has(opts, "M"):
		return nil
	case has(opts, "X"):
		return copyPipe(w, ops)
	case has(opts, "K"):
		// The keys go to the key table of a client and run what they are
		// bound to.
		return []run{{words: ops, mark: dynComputed}}
	}
	var b strings.Builder
	known := true
	for _, k := range ops {
		a := w.args[k]
		switch {
		case has(opts, "F") && strings.Contains(a, "#"):
			// A format, expanded first.
			known = false
		case has(opts, "H"):
			n, err := strconv.ParseUint(a, 16, 64)
			switch {
			case err != nil:
				known = false
			case n <= 0xff:
				b.WriteByte(byte(n))
			}
		case has(opts, "l"):
			b.WriteString(a)
		default:
			s, ok := tmuxKey(a)
			b.WriteString(s)
			known = known && ok
		}
	}
	text, ok := typed(b.String())
	r := run{text: text, words: ops}
	if !known || !ok {
		r.mark = dynComputed
	}
	return []run{r}
}

// copyPipe finds the shell command of copy-pipe, pipe and their kin, which
// copy mode pipes the selection to.
func copyPipe(w words, ops []int) []run {
	if !w.static[ops[0]] {
		return []run{{words: ops[:1]}}
	}
	if name := w.args[ops[0]]; !strings.HasPrefix(name, "copy-pipe") && !strings.HasPrefix(name, "pipe") {
		return nil
	}
	for _, k := range ops[1:] {
		switch {
		case !w.static[k]:
			return []run{{words: []int{k}}}
		case !strings.HasPrefix(w.args[k], "-"):
			return []run{formatShell(w, k)}
		}
	}
	return nil
}

// tmuxKey is what send-keys types for a word as tmux 3.7 reads it: the
// word itself when it names no key, a character, a newline for Enter, C-m,
// ^M and C-j, a space for Space, ^C for C-c. ok is false for any other key:
// a tab, an arrow, a deletion and the like do what the line does not show.
func tmuxKey(a string) (string, bool) {
	switch {
	case strings.EqualFold(a, "None"):
		return a, true
	case strings.EqualFold(a, "Any"):
		return "", false
	case strings.HasPrefix(a, "0x"):
		u, err := strconv.ParseUint(a[2:], 16, 32)
		switch {
		case err != nil:
			return "", false
		case u < ' ':
			return string(rune(u)), true
		case !utf8.ValidRune(rune(u)):
			return a, true
		}
		return string(rune(u)), true
	}
	ctrl, other, rest := false, false, a
	if len(a) > 1 && a[0] == '^' {
		ctrl, rest = true, a[1:]
	}
	for len(rest) > 1 && rest[1] == '-' {
		switch rest[0] {
		case 'C', 'c':
			ctrl = true
		case 'M', 'm', 'S', 's':
			other = true
		default:
			return a, true
		}
		rest = rest[2:]
	}
	if rest == "" {
		return a, true
	}
	if r, n := utf8.DecodeRuneInString(rest); n == len(rest) {
		switch {
		case r < ' ':
			return a, true
		case other:
			return "", false
		case !ctrl:
			return rest, true
		case r == '?':
			return "\x7f", true
		case r >= '@' && r <= '_', r >= 'a' && r <= 'z':
			return string(r & 0x1f), true
		}
		return "", false
	}
	if !tmuxKeyName(rest) {
		return a, true
	}
	if !ctrl && !other {
		switch strings.ToLower(rest) {
		case "enter", "kpenter", "[lf]":
			return "\n", true
		case "space":
			return " ", true
		}
	}
	return "", false
}

// tmuxKeyName tells whether s names a key of tmux 3.7 that is no character:
// the names of its table, in any case, those of the mouse and User0 and on.
func tmuxKeyName(s string) bool {
	l := strings.ToLower(s)
	if slices.Contains(tmuxKeyNames, l) {
		return true
	}
	for _, p := range []string{"mousedown", "mouseup", "mousedrag", "wheelup", "wheeldown", "secondclick", "doubleclick", "tripleclick"} {
		if strings.HasPrefix(l, p) {
			return true
		}
	}
	if u, ok := strings.CutPrefix(s, "User"); ok && u != "" && strings.IndexByte("0123456789+- \t", u[0]) >= 0 {
		return true
	}
	return false
}

// tmuxKeyNames are the names of keys in the table of tmux 3.7, in lower case.
var tmuxKeyNames = strings.Fields(`f1 f2 f3 f4 f5 f6 f7 f8 f9 f10 f11 f12 ic insert dc delete
	home end npage pagedown pgdn ppage pageup pgup btab space bspace tab enter escape up down left
	right [nul] [soh] [stx] [etx] [eot] [enq] [asc] [bel] [bs] [lf] [vt] [ff] [so] [si] [dle] [dc1]
	[dc2] [dc3] [dc4] [nak] [syn] [etb] [can] [em] [sub] [fs] [gs] [rs] [us]
	kp/ kp* kp- kp7 kp8 kp9 kp+ kp4 kp5 kp6 kp1 kp2 kp3 kpenter kp0 kp.`)

// formatShell is the shell command in word k, which tmux expands as a
// format first: what a format puts in it is not in the line.
func formatShell(w words, k int) run {
	r := run{text: w.args[k], words: []int{k}}
	if formatted(w.args[k]) {
		r.mark = dynComputed
	}
	return r
}

// formatted tells whether s holds a format tmux replaces with text the line
// does not hold: #{…}, #(…) or one of #S, #W and their kin.
func formatted(s string) bool {
	for i := 0; i+1 < len(s); i++ {
		if s[i] != '#' {
			continue
		}
		switch c := s[i+1]; {
		case c == '#':
			i++
		case c == '{', c == '(', c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z':
			return true
		}
	}
	return false
}

// formats returns the commands of #(…) in the static words of tmux, which
// run wherever tmux expands a word as a format: in a status line, a shell
// command of run-shell, the message of display-message and so on.
func formats(w words) []run {
	var rs []run
	for k, a := range w.args {
		if !w.static[k] {
			continue
		}
		for _, c := range hashParens(a) {
			r := run{text: c, words: []int{k}}
			if formatted(c) {
				r.mark = dynComputed
			}
			rs = append(rs, r)
		}
	}
	return rs
}

// hashParens returns the commands of #(…) in s, each up to the first ")"
// out of a #{…}, as tmux's format_skip finds it; one left open runs to the
// end of s.
func hashParens(s string) []string {
	var out []string
	for {
		i := strings.Index(s, "#(")
		if i < 0 {
			return out
		}
		s = s[i+2:]
		end, braces := len(s), 0
	scan:
		for j := 0; j < len(s); j++ {
			switch {
			case s[j] == '#' && j+1 < len(s) && strings.IndexByte(",#{}:", s[j+1]) >= 0:
				if s[j+1] == '{' {
					braces++
				}
				j++
			case s[j] == '}':
				braces--
			case s[j] == ')' && braces <= 0:
				end = j
				break scan
			}
		}
		out = append(out, s[:end])
		s = s[end:]
	}
}
