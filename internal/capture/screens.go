package capture

import "bytes"

// FullScreen stands in the text for what a full-screen program (vim, less,
// htop) drew on the alternate screen: a picture, not text, and gone from the
// terminal once the program is.
const FullScreen = "[full-screen interactive program; output not captured]"

var screenOff = [][]byte{[]byte("\x1b[?1049l"), []byte("\x1b[?1047l"), []byte("\x1b[?47l")}

// screenStretch is what a text Buffer keeps of one stretch on the alternate
// screen: the switches alone, which Clean turns into FullScreen.
var screenStretch = []byte("\x1b[?1049h\x1b[?1049l")

// NewText is a Buffer for the text of a command's output, for Clean. What a
// full-screen program draws on the alternate screen is not kept, only where
// it was: an hour in htop inside an ssh session takes no room from the
// session's text, and is one line in it.
func NewText(headCap, tailCap int) *Buffer {
	return &Buffer{headCap: headCap, tailCap: tailCap, text: true}
}

// writeText keeps p, but for the stretches on the alternate screen. A switch
// split across writes is held back until the next one completes it or not.
func (b *Buffer) writeText(p []byte) {
	data := p
	if len(b.held) > 0 {
		data = append(b.held, p...)
		b.held = nil
	}
	for {
		seqs := altScreenSeqs
		if b.alt {
			seqs = screenOff
		}
		i, n := firstOf(data, seqs)
		if i < 0 {
			k := partial(data, seqs)
			if !b.alt {
				b.store(data[:len(data)-k])
			}
			b.held = append(b.held, data[len(data)-k:]...)
			return
		}
		if b.alt {
			b.store(screenStretch) // where the program was, now it is over
		} else {
			b.store(data[:i])
		}
		b.alt, b.altScreen = !b.alt, true
		data = data[i+n:]
	}
}

// firstOf finds the first of seqs in data: where it starts and its length.
// They all begin with \e[?, which it goes along: a search for each to the
// end of data would make a stream of many screens quadratic.
func firstOf(data []byte, seqs [][]byte) (int, int) {
	for i := 0; ; i++ {
		j := bytes.Index(data[i:], privateCSI)
		if j < 0 {
			return -1, 0
		}
		i += j
		for _, s := range seqs {
			if bytes.HasPrefix(data[i:], s) {
				return i, len(s)
			}
		}
	}
}

var privateCSI = []byte("\x1b[?")

// partial is the length of the longest end of data that begins one of seqs.
func partial(data []byte, seqs [][]byte) int {
	longest := 0
	for _, s := range seqs {
		longest = max(longest, len(s))
	}
	for k := min(len(data), longest-1); k > 0; k-- {
		for _, s := range seqs {
			if len(s) > k && bytes.HasPrefix(s, data[len(data)-k:]) {
				return k
			}
		}
	}
	return 0
}

func isScreenOn(seq []byte) bool {
	for _, s := range altScreenSeqs {
		if bytes.Equal(seq, s) {
			return true
		}
	}
	return false
}
