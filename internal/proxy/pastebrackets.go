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
// mode on: readline's at the prompt, and the proxy's own while it reads
// the keys, see pasteMode.
type keySeq struct {
	part  []byte      // an escape sequence cut by the last read
	gen   int         // which part the timer is for
	timer *time.Timer // takes part for what it is after escWait
	paste bool        // inside a paste kept from the shell
	text  bool        // ... which is typed as the form's Other
	last  time.Time   // when the paste last sent anything

	shell pasteScan // the pastes in the keys that go on to the shell
	mode  pasteMode // the terminal's bracketed paste mode
	other otherKeys // the terminal's modifyOtherKeys, for Shift+Enter at the prompt (shiftenter.go)
}

// escWait is how long a read may follow the one that cut an escape
// sequence: longer than reads of one paste take, short of a late Esc.
var escWait = 100 * time.Millisecond

// pasteGap ends a paste whose end never came: a terminal sends it at once.
const pasteGap = time.Second

var (
	pasteStart = []byte("\x1b[200~")
	pasteEnd   = []byte("\x1b[201~")
	pasteOff   = []byte("\x1b[?2004l")
)

// readsKeys reports whether the proxy takes the keys itself, rather than
// passing them on to the shell; syncPaste follows whatever changes it.
// Called under p.mu.
func (t *console) readsKeys() bool { return t.holding() || t.ask != nil || t.form != nil }

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
			n := len(b) - endCut(b, pasteEnd)
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
		p.syncPaste()
	}
}

// pasteText is what of a paste a reader of keys gets: the form's Other,
// which the cursor was on when the paste began, gets it as text on one
// line; anything else, nothing. Called under p.mu.
func (t *console) pasteText(b []byte) []byte {
	if !t.seq.text || t.form == nil || t.holding() || t.ask != nil {
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

// endCut is how many bytes at the end of b may begin seq.
func endCut(b, seq []byte) int {
	for n := min(len(b), len(seq)-1); n > 0; n-- {
		if bytes.HasPrefix(seq, b[len(b)-n:]) {
			return n
		}
	}
	return 0
}

// pasteMode is the terminal's bracketed paste mode. Readline turns it off
// before it runs a line, a request too, and on again at the next prompt:
// meanwhile a paste comes without brackets, and to a question, the form,
// the panes or the viewer it would be keys, y in it answering Yes and the
// rest, a newline in it, going on to the shell to run. So while the proxy
// reads the keys it turns the mode on itself, and once it stops it gives
// back the one the shell's output last set. The mode is the terminal's,
// not a screen's: the alternate screen of the viewer and the panes neither
// keeps nor gives it back.
type pasteMode struct {
	shell bool   // on, as the shell's output last set it
	on    bool   // the proxy turned it on to read the keys
	again bool   // the shell turned it off since: on again after its output
	cut   []byte // a sequence the shell's last write cut short
}

var modeSet = []byte("\x1b[?") // a private mode set or reset follows

// syncPaste turns the mode on when the proxy has begun to read the keys,
// and gives the shell's back when it has stopped; modifyOtherKeys goes the
// other way. Called under p.mu after whatever opens or closes the viewer,
// the panes, a question or the form.
func (t *console) syncPaste() {
	t.setPaste(t.readsKeys())
	t.syncOther()
}

// setPaste is syncPaste with whether the proxy reads the keys given. The
// mode goes to the terminal at once, past what the viewer holds: the keys
// are read now. Called under p.mu.
func (t *console) setPaste(on bool) {
	m := &t.seq.mode
	if m.on == on {
		return
	}
	m.on, m.again = on, false
	if on || m.shell {
		t.write(pasteOn)
	} else {
		t.write(pasteOff)
	}
}

// pasteOutput follows the mode through the shell's output b, of which show
// went to the terminal or to what it holds. Turned off while the proxy
// reads the keys, the mode goes on again after it, once show leaves no
// sequence open that it would break. Called under p.mu.
func (t *console) pasteOutput(b, show []byte) {
	m := &t.seq.mode
	set := m.feed(b)
	if set && m.on && !m.shell {
		m.again = true
	}
	if m.again && openSeq(show) == len(show) {
		m.again = false
		t.emit(pasteOn)
	}
	t.otherOutput(set && m.shell, show) // modifyOtherKeys, see shiftenter.go
}

// feed keeps the mode the shell's output b sets, if it sets it, and
// reports whether it does, either way.
func (m *pasteMode) feed(b []byte) bool {
	if m.cut != nil {
		b = append(m.cut, b...)
		m.cut = nil
	}
	set := false
	for {
		i := bytes.Index(b, modeSet)
		if i < 0 {
			break
		}
		j := i + len(modeSet)
		for j < len(b) && (b[j] >= '0' && b[j] <= '9' || b[j] == ';') {
			j++
		}
		if j == len(b) {
			if j-i <= maxSeq {
				m.cut = bytes.Clone(b[i:])
			}
			return set
		}
		if b[j] == 'h' || b[j] == 'l' {
			for _, n := range bytes.Split(b[i+len(modeSet):j], []byte{';'}) {
				if string(n) == "2004" {
					m.shell, set = b[j] == 'h', true
				}
			}
		}
		b = b[j:]
	}
	if n := endCut(b, modeSet); n > 0 {
		m.cut = bytes.Clone(b[len(b)-n:])
	}
	return set
}
