package proxy

import (
	"bytes"
	"time"
)

// pasteScan follows the bracketed pastes through the keys that go on to
// the shell as they come, brackets cut by the reads too. A key the proxy
// takes for itself there, Ctrl+O at the prompt or one that sends a signal
// before it, is text inside a paste: Alacritty leaves Ctrl+C out of what
// it pastes, other terminals do not.
type pasteScan struct {
	in   bool      // inside a paste
	n    int       // bytes of the next bracket the last read ended in
	last time.Time // when the paste last sent anything
}

// find returns where in b the first of keys outside a paste is, -1 if
// none, following the pastes through the whole of b.
func (s *pasteScan) find(b []byte, keys ...byte) int {
	now := time.Now()
	if s.in && now.Sub(s.last) > pasteGap {
		s.in, s.n = false, 0 // its end never came
	}
	at := -1
	for i, c := range b {
		bracket := pasteStart
		if s.in {
			bracket = pasteEnd
		}
		switch {
		case c == bracket[s.n]:
			if s.n++; s.n == len(bracket) {
				s.in, s.n = !s.in, 0
			}
			continue
		case c == 0x1b:
			s.n = 1
			continue
		}
		s.n = 0
		if at < 0 && !s.in && bytes.IndexByte(keys, c) >= 0 {
			at = i
		}
	}
	s.last = now
	return at
}
