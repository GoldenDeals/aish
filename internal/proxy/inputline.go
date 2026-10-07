package proxy

import (
	"fmt"

	"github.com/mattn/go-runewidth"
)

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
// scrolling changes none of them. Till the first key every new line takes
// the status down with it: the prompt is the last line printed before the
// input, below what PROMPT_COMMAND prints and the first lines of a prompt
// of several. Output the model does not know (absolute positioning, the
// alternate screen) ends it: the status is erased and stays so till the
// next prompt.
type inputLine struct {
	cols, rows  int
	s           int // the status's first cell; it ends at cols-2
	text, color string

	row, col int
	wrap     bool        // a character went to the last column: the next one goes to the next line
	ends     map[int]int // per row, the right edge of what is printed there, the status aside
	saved    [2]int      // the cursor \e7, \e[s or ?1048h saved
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

// submit erases the status when Enter is pressed on an empty line: the
// prompt it was drawn for is left behind, and the next one gets its own.
// With text on the line the status is already gone.
func (l *inputLine) submit() []byte {
	if !l.shown || l.lost || l.wrap {
		return nil
	}
	return l.erase(nil)
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

// dropLine stops following the input line; what it held back of a
// sequence cut short goes to the terminal still.
func (t *console) dropLine() {
	if t.line != nil {
		t.emit(t.line.flush())
		t.line = nil
	}
}
