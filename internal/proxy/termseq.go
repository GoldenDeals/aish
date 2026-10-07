package proxy

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
)

// maxSeq bounds an escape sequence held back for its end: a longer one is
// none the model knows.
const maxSeq = 256

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
		var o op
		for _, m := range strings.Split(params[1:], ";") {
			switch m {
			case "7", "47", "1047", "1049":
				return op{kind: opLost} // no autowrap, the alternate screen
			case "1048":
				// The cursor saved and restored as \e7 and \e8 do, in
				// the one place the terminal has for it.
				o.kind = 'u'
				if final == 'h' {
					o.kind = 's'
				}
			}
		}
		return o
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
