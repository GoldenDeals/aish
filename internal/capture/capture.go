// Package capture turns raw terminal output into text an LLM can read.
package capture

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// Buffer keeps the head and the tail of a stream and drops the middle, so a
// command that prints gigabytes costs bounded memory.
type Buffer struct {
	headCap, tailCap int
	head             []byte
	tail             []byte // ring, oldest byte at tailPos once full
	tailPos          int
	dropped          int
	altScreen        bool
	scan             []byte // last bytes, for sequences split across writes

	text bool   // NewText's: what is drawn on the alternate screen is not kept
	alt  bool   // its stream is on the alternate screen now
	held []byte // its last bytes, which may begin a switch of screens
}

func NewBuffer(headCap, tailCap int) *Buffer {
	return &Buffer{headCap: headCap, tailCap: tailCap}
}

var altScreenSeqs = [][]byte{[]byte("\x1b[?1049h"), []byte("\x1b[?1047h"), []byte("\x1b[?47h")}

func (b *Buffer) Write(p []byte) {
	if b.text {
		b.writeText(p)
		return
	}
	if !b.altScreen {
		probe := append(b.scan, p...)
		for _, s := range altScreenSeqs {
			if bytes.Contains(probe, s) {
				b.altScreen = true
			}
		}
		n := min(len(probe), 8)
		b.scan = append(b.scan[:0], probe[len(probe)-n:]...)
	}
	b.store(p)
}

func (b *Buffer) store(p []byte) {
	if room := b.headCap - len(b.head); room > 0 {
		n := min(room, len(p))
		b.head = append(b.head, p[:n]...)
		p = p[n:]
	}
	for _, c := range p {
		if len(b.tail) < b.tailCap {
			b.tail = append(b.tail, c)
			continue
		}
		b.tail[b.tailPos] = c
		b.tailPos = (b.tailPos + 1) % b.tailCap
		b.dropped++
	}
}

// AltScreen reports whether the program switched to the alternate screen
// (vim, less, htop...). Its output is not meaningful as text.
func (b *Buffer) AltScreen() bool { return b.altScreen }

// Bytes returns head + tail with a marker where data was dropped.
func (b *Buffer) Bytes() []byte {
	out := append([]byte{}, b.head...)
	if b.dropped > 0 {
		out = append(out, fmt.Sprintf("\n[... %d bytes omitted ...]\n", b.dropped)...)
	}
	out = append(out, b.tail[b.tailPos:]...)
	out = append(out, b.tail[:b.tailPos]...)
	if b.alt {
		return append(out, altScreenSeqs[0]...) // and still there
	}
	return append(out, b.held...)
}

// farCol is how far past a line's end Clean lets the cursor go. A terminal
// stops it at its right edge, whose width Clean doesn't know; without a stop
// `\e[99999G` would pad a line with that many spaces.
const farCol = 512

// Clean strips terminal control sequences and applies carriage returns,
// backspaces, erasing, inserting and deleting in line and moving along it
// the way a terminal would render them line by line. Sequences that span
// lines are ignored. What a full-screen program draws on the alternate
// screen is one line, FullScreen, and the main screen goes on below it.
func Clean(raw []byte) string {
	var lines []string
	var line []rune
	col := 0
	put := func(r rune) {
		for len(line) < col {
			line = append(line, ' ')
		}
		if col < len(line) {
			line[col] = r
		} else {
			line = append(line, r)
		}
		col++
	}
	flush := func() {
		lines = append(lines, strings.TrimRight(string(line), " "))
		line, col = line[:0], 0
	}
	// screen takes the stretch on the alternate screen that rest begins and
	// returns its length. Programs nothing on the main screen came between
	// share their line: a loop of them is not a line each.
	screen := func(rest []byte) int {
		if len(line) > 0 {
			flush()
		}
		line, col = line[:0], 0
		k := len(lines) - 1
		for k >= 0 && lines[k] == "" {
			k--
		}
		if k < 0 || lines[k] != FullScreen {
			lines = append(lines, FullScreen)
		}
		j, n := firstOf(rest, screenOff)
		if j < 0 {
			return len(rest) // never left
		}
		return j + n
	}
	for i := 0; i < len(raw); {
		c := raw[i]
		switch {
		case c == 0x1b:
			n := escapeLen(raw[i:])
			if isScreenOn(raw[i : i+n]) {
				i += n + screen(raw[i+n:])
				continue
			}
			final, arg, ok := csi(raw[i : i+n])
			switch {
			case !ok: // colors, titles, moves to other lines
			case final == 'K' && arg == 0: // to the end of the line
				line = line[:min(col, len(line))]
			case final == 'K' && arg == 1: // from its start through the cursor
				for j := 0; j <= col && j < len(line); j++ {
					line[j] = ' '
				}
			case final == 'K' && arg == 2: // all of it; the cursor stays
				line = line[:0]
			case final == 'G':
				col = min(max(arg, 1)-1, len(line)+farCol)
			case final == 'C':
				col = min(col+max(arg, 1), max(col, len(line)+farCol))
			case final == 'D':
				col = max(col-max(arg, 1), 0)
			case final == '@': // blanks inserted at the cursor push the rest right
				if col < len(line) {
					line = slices.Insert(line, col, slices.Repeat([]rune{' '}, min(max(arg, 1), farCol))...)
				}
			case final == 'P': // deleted at the cursor, the rest comes left
				if col < len(line) {
					line = slices.Delete(line, col, min(col+max(arg, 1), len(line)))
				}
			case final == 'X': // erased from the cursor on; it stays
				for j := col; j < min(col+max(arg, 1), len(line)); j++ {
					line[j] = ' '
				}
			}
			i += n
			continue
		case c == '\n':
			flush()
		case c == '\r':
			col = 0
		case c == '\b':
			if col > 0 {
				col--
			}
		case c == '\t':
			put('\t')
		case c < 0x20 || c == 0x7f:
			// other control characters are invisible
		default:
			r, size := utf8.DecodeRune(raw[i:])
			put(r)
			i += size
			continue
		}
		i++
	}
	if len(line) > 0 {
		flush()
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

// escapeLen returns the length of the escape sequence at the start of s.
func escapeLen(s []byte) int {
	if len(s) < 2 {
		return len(s)
	}
	switch s[1] {
	case '[': // CSI: parameters then a final byte in 0x40..0x7e
		for i := 2; i < len(s); i++ {
			if s[i] >= 0x40 && s[i] <= 0x7e {
				return i + 1
			}
		}
		return len(s)
	case ']', 'P', '_', '^': // OSC/DCS/APC/PM: until BEL or ST
		for i := 2; i < len(s); i++ {
			if s[i] == 0x07 {
				return i + 1
			}
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
		}
		return len(s)
	case '(', ')', '*', '+', '#', '%': // charset selection etc.: one more byte
		return min(3, len(s))
	default:
		return 2
	}
}

// csi returns the final byte of a CSI sequence s and its first parameter, 0
// when omitted. Sequences with private or intermediate bytes are not ok:
// Clean has no use for them.
func csi(s []byte) (final byte, arg int, ok bool) {
	if len(s) < 3 || s[1] != '[' {
		return 0, 0, false
	}
	final = s[len(s)-1]
	if final < 0x40 || final > 0x7e {
		return 0, 0, false // cut off
	}
	first := true
	for _, c := range s[2 : len(s)-1] {
		switch {
		case c == ';':
			first = false
		case c >= '0' && c <= '9':
			if first {
				arg = min(arg*10+int(c-'0'), 1<<20)
			}
		default:
			return 0, 0, false
		}
	}
	return final, arg, true
}

// Truncate keeps the beginning and the end of s within max bytes.
func Truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	head := max * 2 / 5
	tail := max - head
	h := s[:head]
	for !utf8.ValidString(h) && len(h) > 0 {
		h = h[:len(h)-1]
	}
	t := s[len(s)-tail:]
	for !utf8.ValidString(t) && len(t) > 0 {
		t = t[1:]
	}
	return fmt.Sprintf("%s\n[... %d bytes omitted ...]\n%s", h, len(s)-len(h)-len(t), t)
}
