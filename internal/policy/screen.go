package policy

import (
	"slices"
	"strings"
)

// screenRuns finds the code of screen, as screen 4.9 reads its words: the
// command its new window runs as it is, and with -X or -Q the code of the
// command of screen it sends to a session.
func screenRuns(w words) []run {
	cmd, ctl, list, vals := screenRead(w.args)
	var rs []run
	if lw := w.loose(cmd, func(i int) bool { return vals[i] }); len(lw) > 0 {
		rs = append(rs, run{words: lw})
	}
	switch {
	case list, cmd == len(w.args):
	case ctl:
		rs = append(rs, elsewhere(screenCommand(whole(w).from(cmd)))...)
	default:
		r := w.argv(indexes(cmd, len(w.args)))
		rs = append(rs, r)
	}
	return rs
}

// screenRead reads the options of screen as its main does: letters in a
// word, the session of -r, -R, -x, -d, -D, -S, -ls and -wipe in the next
// word on their terms, and the values of -p, -c and -e in the rest of the
// word or the next one, of -h, -t, -T, -s and -Logfile in the next. It
// returns the index of the first operand, whether it is a command of screen
// (-X, -Q), whether screen only lists sessions, and the words of values.
func screenRead(args []string) (cmd int, ctl, list bool, vals map[int]bool) {
	vals = map[int]bool{}
	sock := false
	i := 0
	take := func() {
		if i+1 < len(args) {
			i++
			vals[i] = true
		}
	}
words:
	for ; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			i++
			break words
		case a == "--version", a == "--help":
			return len(args), false, true, vals
		case a == "-Logfile":
			take()
			continue
		case !strings.HasPrefix(a, "-"):
			break words
		}
		for j := 1; j < len(a); j++ {
			switch a[j] {
			case 'p', 'c', 'e':
				if j+1 == len(a) {
					take()
				}
				continue words
			case 'h', 't', 'T', 's':
				take()
			case 'S':
				if !sock {
					take()
					sock = true
				}
			case 'f':
				if j+1 < len(a) && strings.IndexByte("n0y1a", a[j+1]) >= 0 {
					j++
				}
			case 'l':
				if j+1 < len(a) && strings.IndexByte("n0y1a", a[j+1]) >= 0 {
					j++
				} else if j+1 < len(a) && (a[j+1] == 's' || a[j+1] == 'i') {
					return len(args), false, true, vals
				}
			case 'w':
				if a[j+1:] == "ipe" {
					return len(args), false, true, vals
				}
			case 'r', 'R', 'x':
				if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") && !sock {
					take()
					sock = true
				}
			case 'd', 'D':
				if i+2 == len(args) && !strings.HasPrefix(args[i+1], "-") && !sock {
					take()
					sock = true
				}
			case 'X', 'Q':
				ctl = true
			case 'v':
				return len(args), false, true, vals
			}
		}
	}
	return i, ctl, false, vals
}

// screenCommand finds the code of a command of screen sent to a session:
// what stuff types at its window, the program of exec, screen and backtick,
// and the command bind and at run; eval, paste and their kin are marked.
// Screen reads each word anew as in double quotes, expanding $VAR, ^X and
// backslashes.
func screenCommand(c sub) []run {
	if len(c.idx) == 0 {
		return nil
	}
	if !c.w.static[0] {
		return c.at([]run{{words: []int{0}}})
	}
	name, ok := screenWord(c.w.args[0])
	if !ok {
		return []run{{mark: dynComputed}}
	}
	name = strings.TrimPrefix(strings.TrimPrefix(name, "@"), "-")
	args := c.from(1)
	n := len(args.idx)
	switch name {
	case "stuff":
		switch {
		case n == 0:
			return nil
		case n > 1:
			// -k types a key of the terminal.
			return []run{{mark: dynComputed}}
		}
		s, ok := screenWord(args.w.args[0])
		text, plain := typed(s)
		r := run{text: text, words: []int{0}}
		if !ok || !plain {
			r.mark = dynComputed
		}
		return args.at([]run{r})
	case "exec":
		switch {
		case n == 0:
			return nil
		case !args.w.static[0]:
			return args.at([]run{{words: []int{0}}})
		}
		d, ok := screenWords(args)
		if !ok {
			return []run{{mark: dynComputed}}
		}
		if prog := fdpat(d.w.args[0]); prog == "" {
			d = d.from(1)
		} else {
			d.w.args[0] = prog
		}
		return screenArgv(d)
	case "screen":
		d, ok := screenWords(args)
		if !ok {
			return []run{{mark: dynComputed}}
		}
		return screenArgv(screenWindow(d))
	case "backtick":
		d, ok := screenWords(args)
		if !ok {
			return []run{{mark: dynComputed}}
		}
		return screenArgv(d.from(3))
	case "at":
		return screenCommand(args.from(1))
	case "bind":
		for n > 2 && args.w.args[0] == "-c" || n > 1 && args.w.args[0] == "-k" {
			if args.w.args[0] == "-c" {
				args = args.from(1)
			}
			args = args.from(1)
			n = len(args.idx)
		}
		return screenCommand(args.from(1))
	case "eval", "paste", "process":
		return []run{{mark: dynComputed}}
	case "bindkey":
		if n > 1 {
			return []run{{mark: dynComputed}}
		}
	case "source":
		return []run{{mark: dynSource}}
	}
	return nil
}

// screenWindow skips the options and the number of a window of the command
// screen, as DoScreen reads them: an option a word, -t, -T and -h with a
// value in it or the next.
func screenWindow(args sub) sub {
	i := 0
	for ; i < len(args.idx) && strings.HasPrefix(args.w.args[i], "-"); i++ {
		a := args.w.args[i]
		if strings.HasPrefix(a, "--") {
			i++
			break
		}
		if len(a) == 2 && strings.IndexByte("tTh", a[1]) >= 0 {
			i++
		}
	}
	if i < len(args.idx) && args.w.static[i] && numColon(args.w.args[i]) {
		i++
	}
	return args.from(i)
}

// numColon tells whether s is a number of a window: digits, then maybe a
// colon and a title.
func numColon(s string) bool {
	n, _, _ := strings.Cut(s, ":")
	return digits(n)
}

// fdpat returns the program in the first word of exec after its pattern of
// descriptors, "" for none: the word is the pattern alone.
func fdpat(s string) string {
	s = strings.TrimLeft(s, " ")
	p := len(s) - len(strings.TrimLeft(s, ":.!"))
	if p < len(s) && s[p] == '|' {
		p++
	} else {
		for p < len(s) && p > 0 && s[p-1] == '.' {
			p--
		}
	}
	return s[p:]
}

// screenWords reads the words of a command sent to screen as screenWord
// does; ok is false for one that expands $VAR or leaves quotes open.
func screenWords(args sub) (sub, bool) {
	d := args
	d.w.args = slices.Clone(args.w.args)
	for i, a := range d.w.args {
		s, ok := screenWord(a)
		if !ok {
			return d, false
		}
		d.w.args[i] = s
	}
	return d, true
}

// screenArgv is the program screen runs, as it is, from words it has read.
func screenArgv(args sub) []run {
	if len(args.idx) == 0 {
		return nil
	}
	return args.at([]run{args.w.argv(indexes(0, len(args.idx)))})
}

// screenWord reads a word of a command sent to screen as screen 4.9 does,
// as if in double quotes: \n, \r, \t, \\, \" and their kin, \NNN in octal,
// ^X for a control character. ok is false when the word expands $VAR, or
// when a backslash before the closing quote screen puts after it, or before
// one it puts before a quote of the word, leaves the quotes open.
func screenWord(s string) (string, bool) {
	if strings.HasSuffix(s, `\`) || strings.Contains(s, `\"`) {
		return "", false
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '7':
			v, j := 0, i+1
			for ; j < len(s) && j < i+4 && s[j] >= '0' && s[j] <= '7'; j++ {
				v = v<<3 | int(s[j]-'0')
			}
			b.WriteByte(byte(v))
			i = j - 1
		case c == '\\' && i+1 < len(s) && strings.IndexByte(`nrt'"\$#^`, s[i+1]) >= 0:
			i++
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			default:
				b.WriteByte(s[i])
			}
		case c == '$' && i+1 < len(s) && (strings.IndexByte("{:_", s[i+1]) >= 0 || alnum(s[i+1])):
			return "", false
		case c == '^' && i+1 < len(s):
			i++
			if s[i] == '?' {
				b.WriteByte(0x7f)
			} else {
				b.WriteByte(s[i] & 0x1f)
			}
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), true
}

// alnum tells whether c is an ASCII letter or digit.
func alnum(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}
