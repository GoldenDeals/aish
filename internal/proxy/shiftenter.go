package proxy

import (
	"time"
	"unicode/utf8"
)

// otherKeys is xterm's modifyOtherKeys, which the proxy asks the terminal
// for while the shell is at its prompt, for Shift+Enter to put a newline in
// the line: without it terminals send Shift+Enter as Enter. Level 1 changes
// only the keys whose modifier the legacy encoding loses, Shift+Enter,
// Ctrl+Enter or Ctrl+1, to CSI 27;mod;key ~: Esc, Ctrl+letter, Alt+letter,
// Shift+Tab and the cursor and function keys stay as they were. The kitty
// keyboard protocol, the other way to tell Shift+Enter, sends Esc and every
// Ctrl+ and Alt+ key as CSI u even at its least, and tmux does not pass it
// on; modifyOtherKeys it does, with extended-keys on.
//
// The mode is the shell's only at its prompt: from when readline or zle
// takes the terminal after cmd-end, which they show by turning bracketed
// paste on (early.go reads it so too), to cmd-start or ask-start; and only
// while the keys go to the shell, not to the viewer, the panes, a question
// or the form. A full-screen program, a request, what PROMPT_COMMAND runs
// and the shell after aish get the terminal as it was; so does a readline
// with bracketed paste off, bash's before 5.1 by default.
//
// Meanwhile the keys the mode changed reach the shell as the terminal sends
// them without it, and Shift+Enter as Ctrl+V Ctrl+J, a newline readline and
// zle insert as it is. Their CSI u form, which tmux sends with
// extended-keys-format csi-u and a terminal may be set to send, is read
// too. A key inside a paste is the text pasted, and stays: Ctrl+Shift+C
// made Ctrl+C there would interrupt.
type otherKeys struct {
	prompt bool // the shell is at its prompt: cmd-end came, and no cmd-start or ask-start since
	reads  bool // ... where readline or zle has turned bracketed paste on
	on     bool // the proxy asked for the mode

	// The pastes in the keys, as pasteScan follows them.
	paste bool      // inside a paste
	n     int       // bytes of the next bracket the last read ended in
	last  time.Time // when the keys last came
}

var (
	otherKeysOn  = []byte("\x1b[>4;1m")
	otherKeysOff = []byte("\x1b[>4m") // the terminal's own level, 0 unless set otherwise
	// newlineKeys is what Shift+Enter becomes: the key after Ctrl+V goes
	// in the line as it is, in bash's vi command mode too. Where Ctrl+V is
	// bound to no quoted-insert, zsh's vi command mode or the user's own
	// binding, the newline is Enter, as Shift+Enter was.
	newlineKeys = []byte("\x16\n")
)

// The modifiers of CSI 27;mod;key ~ and CSI key;mod u, less one.
const (
	modShift = 1
	modAlt   = 2
	modCtrl  = 4
	modMeta  = 8
	modLocks = 64 | 128 // Caps Lock and Num Lock, in the CSI u of some terminals
)

// promptMarker takes a marker of the shell's: cmd-end leaves the shell at
// its prompt, cmd-start and ask-start take it from there. Called under
// p.mu.
func (t *console) promptMarker(kind string) {
	switch kind {
	case "cmd-end":
		t.atPrompt(true)
	case "cmd-start", "ask-start":
		t.atPrompt(false)
	}
}

// atPrompt is whether the shell is at its prompt, from its markers or its
// end. Called under p.mu.
func (t *console) atPrompt(at bool) {
	o := &t.seq.other
	o.prompt, o.reads = at, false
	t.syncOther()
}

// otherOutput follows the shell's output, of which show went to the
// terminal or to what it holds: reads, its turning bracketed paste on, is
// readline or zle taking the terminal at the prompt, and the keys held
// before the first prompt have gone at it too. The mode goes once show
// leaves no sequence open that it would break. Called under p.mu.
func (t *console) otherOutput(reads bool, show []byte) {
	o := &t.seq.other
	if reads {
		o.reads = o.prompt
	}
	if openSeq(show) == len(show) {
		t.syncOther()
	}
}

// syncOther asks for the mode while the shell's keys are its own at its
// prompt, and gives the terminal its own back once they are not. Called
// under p.mu with syncPaste.
func (t *console) syncOther() {
	o := &t.seq.other
	t.setOther(o.prompt && o.reads && t.size != nil && t.early == nil && !t.readsKeys())
}

// setOther writes the mode at once, past what the viewer holds, as
// setPaste does. Called under p.mu.
func (t *console) setOther(on bool) {
	o := &t.seq.other
	if o.on == on {
		return
	}
	o.on = on
	if on {
		o.paste, o.n = false, 0
		t.write(otherKeysOn)
	} else {
		t.write(otherKeysOff)
	}
}

// shiftEnter gives back the keys b as the terminal sends them without the
// mode, Shift+Enter as newlineKeys, while the proxy has asked for it. A key
// a read cut goes as it came: a terminal writes each one whole, and reads
// cut them only past their buffer, in a paste. Called under p.mu.
func (t *console) shiftEnter(b []byte) []byte {
	o := &t.seq.other
	if !o.on {
		return b
	}
	now := time.Now()
	if o.paste && now.Sub(o.last) > pasteGap {
		o.paste, o.n = false, 0 // its end never came
	}
	o.last = now
	var out []byte
	from := 0 // where b is not yet in out
	for i := 0; i < len(b); {
		if !o.paste && b[i] == 0x1b {
			if n, key := modifiedKey(b[i:]); n > 0 {
				out = append(append(out, b[from:i]...), key...)
				i += n
				from, o.n = i, 0
				continue
			}
		}
		o.follow(b[i])
		i++
	}
	if out == nil {
		return b
	}
	return append(out, b[from:]...)
}

// follow takes the paste brackets through c, as pasteScan.find does.
func (o *otherKeys) follow(c byte) {
	bracket := pasteStart
	if o.paste {
		bracket = pasteEnd
	}
	switch {
	case c == bracket[o.n]:
		if o.n++; o.n == len(bracket) {
			o.paste, o.n = !o.paste, 0
		}
	case c == 0x1b:
		o.n = 1
	default:
		o.n = 0
	}
}

// modifiedKey reads the key b starts with, if it is one in the form
// modifyOtherKeys or CSI u sends: its length and what the terminal sends
// for it without them. It returns 0 for anything else, and for a key cut
// short.
func modifiedKey(b []byte) (int, []byte) {
	if len(b) < 3 || b[0] != 0x1b || b[1] != '[' {
		return 0, nil
	}
	var ps []int
	v, digits := 0, false
	for i := 2; i < len(b); i++ {
		c := b[i]
		switch {
		case c >= '0' && c <= '9':
			if v = v*10 + int(c-'0'); v > utf8.MaxRune {
				return 0, nil
			}
			digits = true
		case c == ';' && digits && len(ps) < 2:
			ps = append(ps, v)
			v, digits = 0, false
		case !digits:
			return 0, nil
		default:
			ps = append(ps, v)
			switch {
			case c == '~' && len(ps) == 3 && ps[0] == 27 && ps[1] >= 1:
				return i + 1, legacyKey(rune(ps[2]), ps[1]-1)
			case c == 'u' && len(ps) == 1:
				return i + 1, legacyKey(rune(ps[0]), 0)
			case c == 'u' && ps[1] >= 1:
				return i + 1, legacyKey(rune(ps[0]), ps[1]-1)
			}
			return 0, nil
		}
	}
	return 0, nil
}

// legacyKey is what a terminal sends for key with the modifiers mods when
// no mode asks for more: xterm's, and X11's for Ctrl. Shift+Enter alone is
// newlineKeys.
func legacyKey(key rune, mods int) []byte {
	mods &^= modLocks
	if key == '\r' && mods == modShift {
		return newlineKeys
	}
	var s []byte
	if mods&(modAlt|modMeta) != 0 {
		s = append(s, 0x1b)
	}
	switch {
	case key == '\t' && mods&modShift != 0:
		return append(s, "\x1b[Z"...)
	case mods&modCtrl != 0:
		key = ctrlKey(key)
	case mods&modShift != 0 && key >= 'a' && key <= 'z':
		key -= 'a' - 'A'
	}
	return utf8.AppendRune(s, key)
}

// ctrlKey is the character Ctrl makes of key, the key itself where it
// makes none.
func ctrlKey(key rune) rune {
	switch {
	case key >= 'a' && key <= 'z':
		return key - 'a' + 1
	case key >= '@' && key <= '_':
		return key - '@'
	case key == ' ', key == '2':
		return 0
	case key >= '3' && key <= '7':
		return key - '3' + 0x1b
	case key == '8', key == '?':
		return 0x7f
	case key == '/':
		return 0x1f
	}
	return key
}
