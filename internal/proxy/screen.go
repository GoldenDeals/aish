package proxy

import "bytes"

// screenSeqs are the terminal sequences Screen reacts to.
var screenSeqs = []struct {
	seq    []byte
	effect int
}{
	{[]byte("\x1b[2J"), effClear},
	{[]byte("\x1b[3J"), effClear},
	// tmux and screen's terminfo `clear`, so Ctrl+L there. ED alone only
	// erases below the cursor; it erases the screen from its top-left corner.
	{[]byte("\x1b[H\x1b[J"), effClear},
	{[]byte("\x1bc"), effClear}, // RIS, `reset`
	{[]byte("\x1b[?1049h"), effAltOn},
	{[]byte("\x1b[?1047h"), effAltOn},
	{[]byte("\x1b[?47h"), effAltOn},
	{[]byte("\x1b[?1049l"), effAltOff},
	{[]byte("\x1b[?1047l"), effAltOff},
	{[]byte("\x1b[?47l"), effAltOff},
}

const (
	effClear = iota
	effAltOn
	effAltOff
)

// Screen follows the terminal's main screen: it reports when it was erased
// (`clear`, Ctrl+L) and ignores erasing done by full-screen programs on the
// alternate screen.
type Screen struct {
	alt     bool
	pending []byte // a possible sequence split across writes
}

// Feed reports whether p erased the main screen, and where in p the output
// that remains on it starts.
func (s *Screen) Feed(p []byte) (cleared bool, from int) {
	data, held := p, len(s.pending)
	if held > 0 {
		data = append(s.pending, p...)
		s.pending = nil
	}
	for i := bytes.IndexByte(data, 0x1b); i >= 0; {
		rest := data[i:]
		for _, q := range screenSeqs {
			if len(rest) < len(q.seq) {
				if bytes.HasPrefix(q.seq, rest) {
					s.pending = append([]byte{}, rest...)
					return cleared, from
				}
				continue
			}
			if !bytes.HasPrefix(rest, q.seq) {
				continue
			}
			switch q.effect {
			case effClear:
				if !s.alt {
					cleared, from = true, max(i+len(q.seq)-held, 0)
				}
			case effAltOn:
				s.alt = true
			case effAltOff:
				s.alt = false
			}
			break
		}
		j := bytes.IndexByte(data[i+1:], 0x1b)
		if j < 0 {
			break
		}
		i += 1 + j
	}
	return cleared, from
}
