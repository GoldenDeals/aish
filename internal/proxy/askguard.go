package proxy

import (
	"bytes"
	"slices"
	"time"
)

// askGuard keeps the keys typed ahead from answering an open question,
// Yes/No or the form of ask_user. While the agent works the user types on,
// the next command for the shell, and a question opens under the fingers:
// the Enter of pwd⏎ would allow the rm -rf it asks about, the y of
// cd mydir too. A key is the question's only after the keyboard was quiet
// for askGrace before it: since the question came on the screen and since
// the key before. Nobody reads a question and answers it quicker, and the
// keys of a line typed on come closer together.
//
// The keys that come without the pause go on as they began. Typed on from
// before the question, or after a character the question has no use for,
// a line begun for the shell, they are typed ahead: they wait, and go to
// the shell once the question is closed, in the order they came and ahead
// of what is typed after the answer, as with no question at all; the
// question does not change for them. Right after a key of the question
// they are the form's, where what is typed in Other and the arrows about
// it come as fast as the user types; to Yes/No, where each key answers or
// turns the answer, they are nothing, nor are they the shell's: of ↑↑⏎
// typed on, ↑ turned the answer, and ↑⏎ would run another command than the
// one meant. Ctrl+C and Ctrl+O act at once, as anywhere. A firm question
// (confirm.go) has its own way with the keys typed ahead: they answer No
// or go nowhere; Yes takes the pause there too.
type askGuard struct {
	since time.Time // the question shown, or the last read of the keyboard
	fresh bool      // the next key came after the keyboard was quiet
	ahead bool      // the keys without a pause are typed ahead
	held  []byte    // the keys typed ahead, for the shell
}

// askGrace is how long the keyboard is quiet before a key that answers.
// Reading a question takes longer: an answer comes a second or more after
// it. The keys of a line typed on come every 120-300ms at 40-100 words a
// minute, and within half a second down to some 25. It is readline's
// keyseq-timeout too, how long it waits for the next key of a sequence.
const askGrace = 500 * time.Millisecond

// newGuard guards a question shown at now.
func newGuard(now time.Time) askGuard { return askGuard{since: now, ahead: true} }

// guard is the open question's, nil without one. Called under p.mu.
func (t *console) guard() *askGuard {
	switch {
	case t.ask != nil:
		return &t.ask.guard
	case t.form != nil:
		return &t.form.guard
	}
	return nil
}

// keyTime is when the keys read now came. Called under p.mu.
func (t *console) keyTime() time.Time {
	if t.keyClock != nil {
		return t.keyClock()
	}
	return time.Now()
}

// keysRead tells the open question's guard of b, a read of the keyboard,
// whoever gets it: the keys of the viewer opened over the question and a
// paste are typing too, and the Enter that comes in one read after a
// paste is no answer. A read that ends a key the last read cut
// (pastebrackets.go) is that key's. Called under p.mu, before wholeKeys.
func (t *console) keysRead(b []byte) {
	g := t.guard()
	if g == nil {
		return
	}
	now := t.keyTime()
	if t.seq.part == nil {
		g.fresh = now.Sub(g.since) >= askGrace
	}
	if t.seq.paste || bytes.HasPrefix(append(slices.Clip(t.seq.part), b...), pasteStart) {
		g.fresh = false // only the first key of a read may be fresh, and the paste is first
	}
	g.since = now
}

// take reports whether the next key of the read is the one after the
// pause: only the first is.
func (g *askGuard) take() bool {
	f := g.fresh
	g.fresh = false
	return f
}

// hold keeps key, typed ahead, for the shell.
func (g *askGuard) hold(key []byte) {
	g.held = append(g.held, key...)
	g.ahead = true
}

// release returns what goes on to the shell as the question closes or the
// request is interrupted: the keys typed ahead, then rest.
func (g *askGuard) release(rest []byte) []byte {
	out := append(g.held, rest...)
	g.held = nil
	return out
}

// releaseKeys gives the shell the keys typed ahead of a question that
// ended with no key, out of time or with its request: no read of the
// keyboard is there to take them along. Called under p.mu.
func (t *console) releaseKeys(g *askGuard) {
	if keys := g.release(nil); len(keys) > 0 && t.toShell != nil {
		_, _ = t.toShell.Write(keys)
	}
}

// nextUnit is nextKey with Ctrl+C and Ctrl+O keys by themselves wherever
// they come: a sequence cut short does not take them along.
func nextUnit(b []byte) (int, formKey, rune) {
	if i := bytes.IndexAny(b, "\x03\x0f"); i == 0 {
		return 1, keyNone, 0
	} else if i > 0 {
		b = b[:i]
	}
	return nextKey(b)
}

// askChar is what key is to Yes/No: the byte of a key of one; for an
// arrow, told by the last byte of its sequence, h, l or Tab (up, down,
// Shift+Tab: in one row either way is the other); 0 for anything else.
func askChar(key []byte) byte {
	switch {
	case len(key) == 1:
		return key[0]
	case key[0] != 0x1b || key[1] != '[' && key[1] != 'O':
		return 0 // a character of more bytes, Alt with a key
	}
	switch key[len(key)-1] {
	case 'D':
		return 'h'
	case 'C':
		return 'l'
	case 'A', 'B', 'Z':
		return '\t'
	}
	return 0
}

// takes reports whether the form has a use for the character r as it is
// now, which press does something with: anything typed in Other, the
// number of a line, a space to check one.
func (f *form) takes(r rune) bool {
	q := f.qs[f.step]
	return f.typing() || r >= '1' && int(r-'1') <= len(q.Options) || r == ' ' && q.MultiSelect
}
