package proxy

// lastLine follows whether the output so far leaves text on the cursor's
// line. Output shown as it came may end inside a line, and the spinner of
// the agent's next turn, which starts with \r and erases to the end of the
// line, would draw over it. Sequences print nothing: many programs end
// with one after their last newline (ls resets its colours, others show
// the cursor again), and those lines are ended. Erasing the line from its
// start empties it; the alternate screen's text is gone when it closes.
type lastLine struct {
	text bool // the line holds text
	col0 bool // the cursor is at the start of the line
	alt  bool // on the alternate screen

	seq   byte   // where the last write left a sequence: 0, ESC, '[' or ']' for a string
	param []byte // the CSI's parameters so far
}

// maxParam bounds the CSI parameters kept: the ones that matter are short.
const maxParam = 16

func (l *lastLine) feed(b []byte) {
	for _, c := range b {
		switch l.seq {
		case 0x1b:
			l.escape(c)
			continue
		case '[':
			if c >= 0x20 && c < 0x40 {
				if len(l.param) < maxParam {
					l.param = append(l.param, c)
				}
				continue
			}
			l.seq = 0
			if c >= 0x40 && c <= 0x7e {
				l.csi(string(l.param), c)
				l.param = l.param[:0]
				continue
			}
			// A control character cuts the sequence short and acts as one.
			l.param = l.param[:0]
		case ']':
			switch c {
			case 0x07:
				l.seq = 0
			case 0x1b:
				l.seq = 0x1b // ST: the backslash that follows ends the escape
			}
			continue
		}
		l.char(c)
	}
}

func (l *lastLine) escape(c byte) {
	switch {
	case c == '[':
		l.seq = '['
	case c == ']' || c == 'P' || c == 'X' || c == '^' || c == '_':
		l.seq = ']'
	case c >= 0x20 && c < 0x30:
		// an intermediate byte: the final one follows
	default:
		l.seq = 0
	}
}

func (l *lastLine) char(c byte) {
	switch {
	case c == 0x1b:
		l.seq = 0x1b
	case l.alt:
	case c == '\n':
		l.text, l.col0 = false, true
	case c == '\r':
		l.col0 = true
	case c == '\t':
		l.col0 = false
	case c < 0x20 || c == 0x7f:
	default:
		l.text, l.col0 = true, false
	}
}

func (l *lastLine) csi(param string, final byte) {
	switch final {
	case 'h', 'l':
		if param == "?1049" || param == "?1047" || param == "?47" {
			l.alt = final == 'h'
		}
	case 'K':
		if !l.alt && (param == "2" || l.col0 && (param == "" || param == "0")) {
			l.text = false
		}
	case 'G':
		if !l.alt {
			l.col0 = param == "" || param == "0" || param == "1"
		}
	}
}
