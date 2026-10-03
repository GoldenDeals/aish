package proxy

import (
	"bytes"
	"time"
)

// keySeq is what the keyboard sent that the proxy keeps from one read to
// the next while it reads the keys itself: the viewer, the panes, a
// question, the form. Readline puts back together an escape sequence a
// read cut, and so must the proxy: the ESC of a paste read alone would be
// Esc, closing the viewer or the form, and the rest, [200~ and the text,
// would go to the shell. A lone Esc is told from a cut sequence by escWait
// passing with nothing more.
//
// A bracketed paste is no keys there: y in it would answer a question, q
// leave the viewer, Enter answer the form, Ctrl+C stop the request, and
// what follows such a key would go to the shell without the brackets, a
// newline running a line. The paste goes nowhere, or as text to the
// form's Other. Brackets come only with the terminal's bracketed paste
// mode on: readline's at the prompt, not while a request runs.
type keySeq struct {
	part  []byte      // an escape sequence cut by the last read
	gen   int         // which part the timer is for
	timer *time.Timer // takes part for what it is after escWait
	paste bool        // inside a paste kept from the shell
	text  bool        // ... which is typed as the form's Other
	last  time.Time   // when the paste last sent anything
}

// escWait is how long a read may follow the one that cut an escape
// sequence: longer than reads of one paste take, short of a late Esc.
var escWait = 100 * time.Millisecond

// pasteGap ends a paste whose end never came: a terminal sends it at once.
const pasteGap = time.Second

var (
	pasteStart = []byte("\x1b[200~")
	pasteEnd   = []byte("\x1b[201~")
)

// readsKeys reports whether the proxy takes the keys itself, rather than
// passing them on to the shell. Called under p.mu.
func (p *Proxy) readsKeys() bool { return p.holding() || p.ask != nil || p.form != nil }

// wholeKeys returns b with the sequence the last read cut put back in
// front of it, and, for a reader of keys, without a sequence b cuts and
// without the pastes. Called under p.mu.
func (p *Proxy) wholeKeys(b []byte) []byte {
	k := &p.seq
	if k.timer != nil {
		k.timer.Stop()
		k.timer = nil
	}
	if k.part != nil {
		b = append(k.part, b...)
		k.part = nil
	}
	now := time.Now()
	if k.paste && now.Sub(k.last) > pasteGap {
		k.paste = false
	}
	if !k.paste && !p.readsKeys() {
		return b // the shell's readline puts its sequences together itself
	}
	var keys []byte
	for len(b) > 0 {
		if !k.paste {
			i := bytes.Index(b, pasteStart)
			if i < 0 {
				break
			}
			keys = append(keys, b[:i]...)
			b = b[i+len(pasteStart):]
			k.paste, k.last = true, now
			k.text = !p.holding() && p.ask == nil && p.form != nil && p.form.f.typing()
			continue
		}
		k.last = now
		i := bytes.Index(b, pasteEnd)
		if i < 0 {
			n := len(b) - endCut(b)
			keys = append(keys, p.pasteText(b[:n])...)
			if n < len(b) {
				k.part = bytes.Clone(b[n:])
			}
			return keys
		}
		keys = append(keys, p.pasteText(b[:i])...)
		b = b[i+len(pasteEnd):]
		k.paste = false
	}
	if !p.readsKeys() {
		return append(keys, b...) // after a paste whose reader is gone
	}
	n := openSeq(b)
	if n < len(b) {
		k.part = bytes.Clone(b[n:])
		k.gen++
		gen := k.gen
		k.timer = time.AfterFunc(escWait, func() { p.loneKeys(gen) })
	}
	return append(keys, b[:n]...)
}

// loneKeys takes the sequence held as gen for what it is, nothing more
// having come: Esc, or a sequence the terminal never ended.
func (p *Proxy) loneKeys(gen int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	k := &p.seq
	if k.part == nil || k.gen != gen {
		return // a read took it
	}
	part := k.part
	k.part, k.timer = nil, nil
	if p.readsKeys() {
		// Nothing of these goes to the shell from a reader of keys; with
		// none left, the Esc was meant for the one gone.
		p.takeKeys(part)
	}
}

// pasteText is what of a paste a reader of keys gets: the form's Other,
// which the cursor was on when the paste began, gets it as text on one
// line; anything else, nothing. Called under p.mu.
func (p *Proxy) pasteText(b []byte) []byte {
	if !p.seq.text || p.form == nil || p.holding() || p.ask != nil {
		return nil
	}
	var s []byte
	for i, c := range b {
		switch {
		case c == '\n' && i > 0 && b[i-1] == '\r':
		case c == '\r' || c == '\n' || c == '\t':
			s = append(s, ' ')
		case c >= 0x20 && c != 0x7f:
			s = append(s, c)
		}
	}
	return s
}

// typing reports whether the cursor is on Other, where keys are text.
func (f *form) typing() bool {
	return !f.done && f.cur[f.step] == len(f.qs[f.step].Options)
}

// openSeq returns where an escape sequence that b ends in before its end
// begins, len(b) if none does: ESC alone, ESC [ with no final byte yet,
// ESC O.
func openSeq(b []byte) int {
	i := bytes.LastIndexByte(b, 0x1b)
	if i < 0 {
		return len(b)
	}
	switch s := b[i+1:]; {
	case len(s) == 0, len(s) == 1 && s[0] == 'O':
		return i
	case s[0] == '[':
		for _, c := range s[1:] {
			if c < 0x20 || c > 0x3f {
				return len(b) // a final byte, or no CSI at all
			}
		}
		return i
	}
	return len(b)
}

// endCut is how many bytes at the end of b may begin the paste's end.
func endCut(b []byte) int {
	for n := min(len(b), len(pasteEnd)-1); n > 0; n-- {
		if bytes.HasPrefix(pasteEnd, b[len(b)-n:]) {
			return n
		}
	}
	return 0
}
