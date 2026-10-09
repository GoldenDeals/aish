package proxy

import "bytes"

// While a question is open, Yes/No or the form of ask_user, the shell's
// output waits for it to close. A job in the background, or the process
// that asked for the question (aish yolo, run by code the agent left to
// the shell), would draw over it: rewrite the question above the choices,
// and the user would answer another one than the proxy asks. The answer
// is safe as it is, the proxy reading it from the keyboard (askKey,
// formKey); the text is now too. The question's own frames go past the
// hold, through emit. Once the question is off the screen, answered or
// not, the output goes there whole and in the order it came: after the
// line the answer leaves, or after the erase.
//
// The hold is no Ctrl+O viewer: the question is on the screen meanwhile,
// and its clock goes on (askclock.go, holding).
//
// The echo of Ctrl+C is the exception. Ctrl+C goes on to the shell, and
// the terminal echoes ^C after the choices, which the request it stops
// takes off the screen with the question (askUser, askForm). Held, the
// echo would come after the erase, and the prompt after it on its line.
// So what came after that Ctrl+C and is its echo, the text on the line
// up to the last ^C, the keys typed ahead with it before, goes to the
// screen right before the erase.

// askHold is the shell's output an open question holds.
type askHold struct {
	out    []byte
	passed bool // a Ctrl+C went on to the shell, ...
	cut    int  // ... and its echo is in out from here
}

// askHoldCap is how much of the held output a question keeps: the end of
// it, which is what the screen would show. A job that writes on while
// nobody answers would take the memory otherwise.
const askHoldCap = 1 << 20

// openHold is the hold of the open question, nil without one. Called
// under p.mu.
func (t *console) openHold() *askHold {
	switch {
	case t.ask != nil:
		return &t.ask.hold
	case t.form != nil:
		return &t.form.hold
	}
	return nil
}

// emitShell is emit for the shell's output, which an open question holds.
// Called under p.mu.
func (t *console) emitShell(b []byte) {
	if h := t.openHold(); h != nil {
		h.add(b)
		return
	}
	t.emit(b)
}

func (h *askHold) add(b []byte) {
	h.out = append(h.out, b...)
	if len(h.out) <= 2*askHoldCap {
		return // dropped by the cap at a time, not at each write
	}
	drop := len(h.out) - askHoldCap
	h.out = append([]byte(nil), h.out[drop:]...)
	if h.cut -= drop; h.cut < 0 {
		h.passed = false // the echo went with what was dropped
	}
}

// interrupted marks where the echo of the Ctrl+C the question passes on
// to the shell begins: the first one's, which a second one's follows.
func (h *askHold) interrupted() {
	if !h.passed {
		h.passed, h.cut = true, len(h.out)
	}
}

// echo takes the echo of the Ctrl+C passed on out of h: what came after
// it up to the last ^C, if all of it stays on the line, text with no
// control bytes; nothing otherwise, and then all of it waits for the
// erase.
func (h *askHold) echo() []byte {
	if !h.passed {
		return nil
	}
	h.passed = false
	after := h.out[h.cut:]
	n := 0
	for n < len(after) && after[n] >= 0x20 && after[n] != 0x7f {
		n++
	}
	i := bytes.LastIndex(after[:n], []byte("^C"))
	if i < 0 {
		return nil
	}
	e := bytes.Clone(after[:i+2])
	h.out = append(h.out[:h.cut], after[i+2:]...)
	return e
}

// release writes what h held, its question off the screen now. Called
// under p.mu.
func (t *console) release(h *askHold) {
	b := h.out
	*h = askHold{}
	if len(b) > 0 {
		t.emit(b)
	}
}
