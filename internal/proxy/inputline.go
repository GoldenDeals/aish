package proxy

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
)

// maxSeq bounds an escape sequence held back for its end: a longer one is
// none the model knows.
const maxSeq = 256

// inputLine keeps the prompt's status off the line being typed: it is on
// the screen while the line is empty and gone while there is text. Readline
// has no hook for its buffer changing, but every change shows in what it
// prints: emptying the line, it brings the cursor back to the end of the
// prompt and erases everything right of it and below. So inputLine reads
// the shell's output from the prompt on, with a model that is no screen,
// only the cursor and how far each line is printed, and adds to the output
// what erases the status and draws it again. The prompt ends where the
// cursor was on the first key.
//
// Rows count from the status's line, the one cmd-end found the cursor on:
// scrolling changes none of them. Output the model does not know (absolute
// positioning, the alternate screen) ends it: the status is erased and
// stays so till the next prompt.
type inputLine struct {
	cols, rows  int
	s           int // the status's first cell; it ends at cols-2
	text, color string

	row, col int
	wrap     bool        // a character went to the last column: the next one goes to the next line
	ends     map[int]int // per row, the right edge of what is printed there, the status aside
	saved    [2]int      // the cursor \e7 or \e[s saved
	held     bool        // by the shell, which has not restored it yet: \e7 is not ours to use
	keyed    bool        // the first key came, the prompt ends at prompt
	prompt   [2]int
	shown    bool // the status is on the screen, whole
	lost     bool // the output went past the model

	seq []byte // the start of a sequence or a character the last write cut short
	str bool   // inside an OSC, DCS or another string, which goes through as is
}

func newInputLine(cols, rows int, text, color string) *inputLine {
	return &inputLine{
		cols:  cols,
		rows:  rows,
		s:     cols - runewidth.StringWidth(text) - 1,
		text:  text,
		color: color,
		ends:  map[int]int{},
	}
}

// draw puts the status on the line the prompt is about to be printed on.
func (l *inputLine) draw() []byte {
	return l.paint(nil)
}

// typed takes the first key at the prompt: the prompt ends under the cursor.
func (l *inputLine) typed() {
	if l.keyed {
		return
	}
	l.keyed = true
	l.prompt = [2]int{l.row, l.col}
	if l.wrap {
		l.prompt = [2]int{l.row + 1, 0}
	}
}

// flush is what feed held back of a sequence cut short, for the terminal
// to get when the line is no longer followed.
func (l *inputLine) flush() []byte {
	b := l.seq
	l.seq = nil
	return b
}

// feed takes the shell's output and returns what the terminal gets: the
// same, with the status erased before anything is written over it and
// drawn or erased at the end as the line has become empty or not.
func (l *inputLine) feed(b []byte) []byte {
	if l.lost {
		return b
	}
	data := b
	if len(l.seq) > 0 {
		data = append(l.seq, b...)
		l.seq = nil
	}
	out := make([]byte, 0, len(data))
	for i := 0; i < len(data); {
		if l.lost {
			return append(out, data[i:]...)
		}
		if l.str {
			j := i
			for j < len(data) && data[j] != '\a' && data[j] != 0x1b && data[j] != 0x18 && data[j] != 0x1a {
				j++
			}
			if j < len(data) {
				l.str = false
				if data[j] != 0x1b {
					j++ // BEL ends the string, CAN and SUB cut it short; ESC goes on as a sequence of its own
				}
			}
			out = append(out, data[i:j]...)
			i = j
			continue
		}
		n, o := scan(data[i:])
		if n == 0 {
			if len(data)-i <= maxSeq {
				l.seq = append([]byte{}, data[i:]...)
				break
			}
			n, o = len(data)-i, op{kind: opLost}
		}
		if l.shown && l.touches(o) {
			out = l.erase(out)
		}
		out = append(out, data[i:i+n]...)
		l.apply(o)
		i += n
		if l.row >= l.rows || l.row <= -l.rows {
			l.lost, l.shown = true, false // the status is off the screen
		}
	}
	return l.settle(out)
}

// settle draws or erases the status as the line is empty or not, once the
// output is between sequences and characters. Not while a character waits
// in the last column to wrap: \e8 may cancel that.
func (l *inputLine) settle(out []byte) []byte {
	if l.lost || l.str || len(l.seq) > 0 || l.wrap {
		return out
	}
	want := !l.held && l.ends[0] < l.s && l.empty()
	switch {
	case want && !l.shown:
		out = l.paint(out)
	case !want && l.shown:
		out = l.erase(out)
	}
	return out
}

// empty reports whether nothing is printed past the end of the prompt, on
// its line or below. Before the first key the line is the prompt's.
func (l *inputLine) empty() bool {
	if !l.keyed {
		return true
	}
	for r, e := range l.ends {
		if r == l.prompt[0] && e > l.prompt[1] || r > l.prompt[0] && e > 0 {
			return false
		}
	}
	return true
}

// paint and erase leave the cursor where it was. \e7 and \e8 rather than
// moving it back: the prompt may leave the color of the typed text on, and
// the status ends with \e[0m.
func (l *inputLine) paint(out []byte) []byte {
	out = append(l.toStatus(out), l.color...)
	out = append(out, l.text...)
	l.shown = true
	return append(out, "\x1b[0m\x1b8"...)
}

func (l *inputLine) erase(out []byte) []byte {
	l.shown = false
	return append(l.toStatus(out), "\x1b[K\x1b8"...)
}

func (l *inputLine) toStatus(out []byte) []byte {
	out = append(out, "\x1b7"...)
	l.saved = [2]int{l.row, l.col}
	switch d := l.row; {
	case d > 0:
		out = fmt.Appendf(out, "\x1b[%dA", d)
	case d < 0:
		out = fmt.Appendf(out, "\x1b[%dB", -d)
	}
	return fmt.Appendf(out, "\x1b[%dG", l.s+1)
}

// op is what a character or a sequence does to the model: kind is the
// final byte of the CSI sequence that does the same, or one of these.
type op struct {
	kind byte
	n    int
}

const (
	opNone   = 0
	opLost   = 1 // nothing the model knows
	opPrint  = 2 // a character n cells wide
	opString = 3 // the start of an OSC, DCS or such string
	opLF     = 4
	opTab    = 5
)

// scan is the length of the character or the sequence b starts with, 0 if
// b ends before it does, and what it does.
func scan(b []byte) (int, op) {
	switch c := b[0]; {
	case c == 0x1b:
		return escape(b)
	case c == '\r':
		return 1, op{kind: 'G', n: 1}
	case c == '\n', c == '\v', c == '\f':
		return 1, op{kind: opLF}
	case c == '\b':
		return 1, op{kind: 'D', n: 1}
	case c == '\t':
		return 1, op{kind: opTab}
	case c < 0x20, c == 0x7f:
		return 1, op{}
	}
	r, n := utf8.DecodeRune(b)
	if r == utf8.RuneError && n <= 1 && !utf8.FullRune(b) {
		return 0, op{}
	}
	return n, op{kind: opPrint, n: runewidth.RuneWidth(r)}
}

func escape(b []byte) (int, op) {
	if len(b) < 2 {
		return 0, op{}
	}
	switch c := b[1]; c {
	case '[':
		j := 2
		for j < len(b) && b[j] >= 0x30 && b[j] <= 0x3f {
			j++
		}
		k := j
		for k < len(b) && b[k] >= 0x20 && b[k] <= 0x2f {
			k++
		}
		if k == len(b) {
			return 0, op{}
		}
		if b[k] < 0x40 || b[k] > 0x7e {
			return k, op{kind: opLost}
		}
		return k + 1, csi(string(b[2:j]), string(b[j:k]), b[k])
	case ']', 'P', '_', '^', 'X':
		return 2, op{kind: opString}
	case '(', ')', '*', '+', '-', '.', '/':
		// A character set picked for G0-G3: ESC ( B and the like.
		if len(b) < 3 {
			return 0, op{}
		}
		return 3, op{}
	case '7':
		return 2, op{kind: 's'}
	case '8':
		return 2, op{kind: 'u'}
	case 'M':
		return 2, op{kind: 'A', n: 1}
	case '=', '>', '\\':
		return 2, op{}
	}
	return 2, op{kind: opLost}
}

func csi(params, inter string, final byte) op {
	if inter != "" {
		if inter == " " && final == 'q' {
			return op{} // the cursor's shape
		}
		return op{kind: opLost}
	}
	if params != "" && params[0] >= '<' && params[0] <= '?' {
		if params[0] != '?' || final != 'h' && final != 'l' {
			return op{kind: opLost}
		}
		for _, m := range strings.Split(params[1:], ";") {
			switch m {
			case "7", "47", "1047", "1049":
				return op{kind: opLost} // no autowrap, the alternate screen
			}
		}
		return op{}
	}
	first, _, _ := strings.Cut(params, ";")
	n, _ := strconv.Atoi(first)
	switch final {
	case 'm', 'n', 'c':
		return op{} // colors, queries
	case 'A', 'B', 'C', 'D', 'G', 'P', '@', 'X':
		return op{kind: final, n: max(n, 1)}
	case 'K':
		return op{kind: 'K', n: n}
	case 'J':
		if n == 0 {
			return op{kind: 'J'}
		}
	case 's', 'u':
		if params == "" {
			return op{kind: final}
		}
	}
	return op{kind: opLost}
}

// touches reports whether o writes into the status's cells, or may.
func (l *inputLine) touches(o op) bool {
	switch o.kind {
	case opLost:
		return true
	case 's':
		return true // the shell's \e7: ours would overwrite what it saves
	case opPrint:
		row, col := l.put(o.n)
		return o.n > 0 && row == 0 && col+o.n > l.s
	case 'K':
		return l.row == 0 && (o.n != 1 || l.col >= l.s)
	case 'J':
		return l.row <= 0
	case 'P', '@':
		return l.row == 0
	case 'X':
		return l.row == 0 && l.col+o.n > l.s
	}
	return false
}

// put is where a character w cells wide goes: on the next line if this one
// has no room left for it.
func (l *inputLine) put(w int) (row, col int) {
	if l.wrap || l.col+w > l.cols {
		return l.row + 1, 0
	}
	return l.row, l.col
}

func (l *inputLine) apply(o op) {
	switch o.kind {
	case opNone:
		return
	case opLost:
		l.lost = true
		return
	case opString:
		l.str = true
		return
	case opPrint:
		if o.n == 0 {
			return // combining: goes with the character before
		}
		l.row, l.col = l.put(o.n)
		l.col += o.n
		l.ends[l.row] = max(l.ends[l.row], l.col)
		l.wrap = l.col == l.cols
		if l.wrap {
			l.col--
		}
		return
	case opLF:
		l.row++
	case opTab:
		l.col = min((l.col/8+1)*8, l.cols-1)
	case 'A':
		l.row -= o.n
	case 'B':
		l.row += o.n
	case 'C':
		l.col = min(l.col+o.n, l.cols-1)
	case 'D':
		l.col = max(l.col-o.n, 0)
	case 'G':
		l.col = min(o.n, l.cols) - 1
	case 'K':
		switch o.n {
		case 0:
			l.ends[l.row] = min(l.ends[l.row], l.col)
		case 1:
			if l.ends[l.row] <= l.col+1 {
				l.ends[l.row] = 0
			}
		case 2:
			l.ends[l.row] = 0
		}
	case 'J':
		l.ends[l.row] = min(l.ends[l.row], l.col)
		for r := range l.ends {
			if r > l.row {
				delete(l.ends, r)
			}
		}
	case 'P':
		if e := l.ends[l.row]; e > l.col {
			l.ends[l.row] = max(l.col, e-o.n)
		}
	case '@':
		if e := l.ends[l.row]; e > l.col {
			l.ends[l.row] = min(l.cols, e+o.n)
		}
	case 'X':
		if e := l.ends[l.row]; l.col < e && l.col+o.n >= e {
			l.ends[l.row] = l.col
		}
	case 's':
		l.saved, l.held = [2]int{l.row, l.col}, true
	case 'u':
		l.row, l.col = l.saved[0], l.saved[1]
		l.held = false
	}
	l.wrap = false
}

// dropLine stops following the input line; what it held back of a
// sequence cut short goes to the terminal still.
func (p *Proxy) dropLine() {
	if p.line != nil {
		p.emit(p.line.flush())
		p.line = nil
	}
}
