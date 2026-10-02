// Package capture turns raw terminal output into text an LLM can read.
package capture

import (
	"bytes"
	"fmt"
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
}

func NewBuffer(headCap, tailCap int) *Buffer {
	return &Buffer{headCap: headCap, tailCap: tailCap}
}

var altScreenSeqs = [][]byte{[]byte("\x1b[?1049h"), []byte("\x1b[?1047h"), []byte("\x1b[?47h")}

func (b *Buffer) Write(p []byte) {
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
	return out
}

// Clean strips terminal control sequences and applies carriage returns and
// backspaces the way a terminal would render them line by line.
func Clean(raw []byte) string {
	var lines []string
	var line []rune
	col := 0
	put := func(r rune) {
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
	for i := 0; i < len(raw); {
		c := raw[i]
		switch {
		case c == 0x1b:
			i += escapeLen(raw[i:])
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
