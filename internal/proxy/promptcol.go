package proxy

import (
	"strconv"
	"strings"
)

// firstCol follows whether the shell's output has left the cursor off the
// first column. The prompt after cmd-end starts where the command's output
// ended, and inputLine counts the prompt's columns from 0: after `printf
// abc` it would be three short and erase the status too late for the text
// typed under it, so drawStatus draws none.
//
// It knows the first column only, not where else the cursor is: text, a
// tab and moving right take it off; \r, \n (the PTY turns it into \r\n),
// moving to column 1 and restoring a cursor saved there bring it back.
// Sequences that leave the column alone (colors, modes, titles, erasing,
// moving up, down or left) change nothing.
type firstCol struct {
	off   bool
	saved bool // off when \e7, \e[s or ?1048h saved the cursor
	alt   bool // off when ?1049h saved it, for ?1049l to restore

	seq []byte // the start of a sequence or a character the last write cut short
	str bool   // inside an OSC, DCS or another string, which prints nothing
}

func (c *firstCol) feed(b []byte) {
	data := b
	if len(c.seq) > 0 {
		data = append(c.seq, b...)
		c.seq = nil
	}
	for i := 0; i < len(data); {
		if c.str {
			j := i
			for j < len(data) && data[j] != '\a' && data[j] != 0x1b && data[j] != 0x18 && data[j] != 0x1a {
				j++
			}
			if j < len(data) {
				c.str = false
				if data[j] != 0x1b {
					j++ // BEL ends the string, CAN and SUB cut it short; ESC goes on as a sequence of its own
				}
			}
			i = j
			continue
		}
		if ch := data[i]; ch >= 0x20 && ch < 0x7f {
			c.off = true // the bulk of any output, without scan's rune widths
			i++
			continue
		}
		n, o := scan(data[i:])
		if n == 0 {
			if len(data)-i <= maxSeq {
				c.seq = append([]byte{}, data[i:]...)
				return
			}
			c.off = true // no sequence anyone knows
			return
		}
		c.apply(data[i:i+n], o)
		i += n
	}
}

func (c *firstCol) apply(seq []byte, o op) {
	if len(seq) > 2 && seq[0] == 0x1b && seq[1] == '[' {
		if f := seq[len(seq)-1]; f >= 0x40 && f <= 0x7e {
			c.csi(string(seq[2:len(seq)-1]), f)
		}
		return
	}
	switch o.kind {
	case opString:
		c.str = true
	case opPrint:
		if o.n > 0 {
			c.off = true
		}
	case opLF:
		if seq[0] == '\n' {
			c.off = false
		}
	case opTab:
		c.off = true
	case 'G': // \r
		c.off = false
	case 's':
		c.saved = c.off
	case 'u':
		c.off = c.saved
	case opLost:
		switch seq[len(seq)-1] {
		case 'E', 'c': // NEL, RIS
			c.off = false
		case '9': // DECFI
			c.off = true
		}
	}
}

// csi takes a CSI sequence by its parameters (the intermediate bytes
// included) and final byte.
func (c *firstCol) csi(params string, final byte) {
	if params != "" && params[0] == '?' {
		if final == 'h' || final == 'l' {
			c.mode(params[1:], final == 'h')
		}
		return
	}
	if params != "" && params[0] >= '<' && params[0] <= '>' ||
		strings.ContainsFunc(params, func(r rune) bool { return r >= 0x20 && r <= 0x2f }) {
		return // keyboard protocols, queries, the cursor's shape
	}
	first, rest, _ := strings.Cut(params, ";")
	switch final {
	case 'G', '`':
		n, _ := strconv.Atoi(first)
		c.off = n > 1
	case 'H', 'f':
		col, _, _ := strings.Cut(rest, ";")
		n, _ := strconv.Atoi(col)
		c.off = n > 1
	case 'E', 'F', 'r': // the start of a line below or above, home
		c.off = false
	case 'C', 'I', 'a', 'b':
		c.off = true
	case 's':
		if params == "" {
			c.saved = c.off
		} else {
			c.off = true // DECSLRM: the cursor goes home, to the left margin
		}
	case 'u':
		if params == "" {
			c.off = c.saved
		}
	}
}

// mode takes the private modes set or reset that move the cursor: the
// alternate screen and saving the cursor.
func (c *firstCol) mode(params string, set bool) {
	for _, m := range strings.Split(params, ";") {
		switch m {
		case "1048":
			if set {
				c.saved = c.off
			} else {
				c.off = c.saved
			}
		case "1049":
			if set {
				c.alt = c.off
			} else {
				c.off = c.alt
			}
		case "47", "1047":
			if !set {
				c.off = true // the cursor stays where the alternate screen had it
			}
		}
	}
}
