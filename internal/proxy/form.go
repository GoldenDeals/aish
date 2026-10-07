package proxy

import (
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/GoldenDeals/aish/internal/agent"
)

const (
	bold    = "\x1b[1m"
	cyan    = "\x1b[36m"
	reverse = "\x1b[7m"
)

// form is ask_user on the terminal: the questions, one at a time, and what
// the user did with them so far. Keys go in, the frame to draw comes out;
// the terminal and the lock are the proxy's (askForm), so that the form is
// tested on bytes alone.
type form struct {
	qs   []agent.Question
	step int // the question shown
	// By question: the line under the cursor, len(Options) being Other;
	// the options checked (multi_select); the line answered (a single
	// choice); what was typed as Other.
	cur    []int
	picked [][]bool
	chosen []int
	other  []string

	done, cancelled bool
}

func newForm(qs []agent.Question) *form {
	f := &form{
		qs: qs, cur: make([]int, len(qs)), picked: make([][]bool, len(qs)),
		chosen: make([]int, len(qs)), other: make([]string, len(qs)),
	}
	for i, q := range qs {
		f.picked[i] = make([]bool, len(q.Options))
	}
	return f
}

type formKey int

const (
	keyNone formKey = iota
	keyUp
	keyDown
	keyBack // ←
	keyEnter
	keyErase // Backspace
	keyEsc
	keyRune
)

// nextKey reads the key at the start of b and returns its length, and the
// rune of a keyRune.
func nextKey(b []byte) (int, formKey, rune) {
	switch c := b[0]; {
	case c == 0x1b:
		if len(b) == 1 || b[1] < 0x20 {
			return 1, keyEsc, 0
		}
		arrow := func(final byte) formKey {
			switch final {
			case 'A':
				return keyUp
			case 'B':
				return keyDown
			case 'D':
				return keyBack
			}
			return keyNone
		}
		switch b[1] {
		case '[':
			i := 2
			for i < len(b) && (b[i] < 0x40 || b[i] > 0x7e) {
				i++
			}
			if i == len(b) {
				return i, keyNone, 0
			}
			return i + 1, arrow(b[i]), 0
		case 'O':
			if len(b) < 3 {
				return len(b), keyNone, 0
			}
			return 3, arrow(b[2]), 0
		}
		// Alt and a key: not Esc, which would throw away all the answers.
		_, size := utf8.DecodeRune(b[1:])
		return 1 + size, keyNone, 0
	case c == '\r' || c == '\n':
		return 1, keyEnter, 0
	case c == 0x7f || c == 0x08:
		return 1, keyErase, 0
	case c < 0x20:
		return 1, keyNone, 0
	}
	r, size := utf8.DecodeRune(b)
	if r == utf8.RuneError {
		return size, keyNone, 0
	}
	return size, keyRune, r
}

// feed takes what the user typed and reports whether the form is over,
// answered or cancelled, and how much of b it took: keys after the last
// Enter are not the form's.
func (f *form) feed(b []byte) (n int, done bool) {
	for n < len(b) && !f.done {
		size, k, r := nextKey(b[n:])
		n += size
		f.press(k, r)
	}
	return n, f.done
}

func (f *form) press(k formKey, r rune) {
	q := f.qs[f.step]
	other := len(q.Options)
	cur := &f.cur[f.step]
	typing := *cur == other
	switch k {
	case keyUp:
		*cur = max(*cur-1, 0)
	case keyDown:
		*cur = min(*cur+1, other)
	case keyBack:
		f.back()
	case keyEsc:
		f.done, f.cancelled = true, true
	case keyEnter:
		f.enter()
	case keyErase:
		if s := f.other[f.step]; typing && s != "" {
			_, size := utf8.DecodeLastRuneInString(s)
			f.other[f.step] = s[:len(s)-size]
		} else {
			f.back()
		}
	case keyRune:
		switch {
		case typing:
			f.other[f.step] += string(r)
		case r >= '1' && int(r-'1') <= other:
			*cur = int(r - '1')
			if q.MultiSelect && *cur < other {
				f.picked[f.step][*cur] = !f.picked[f.step][*cur]
			}
		case r == ' ' && q.MultiSelect:
			f.picked[f.step][*cur] = !f.picked[f.step][*cur]
		}
	}
}

func (f *form) back() {
	if f.step > 0 {
		f.step--
	}
}

// enter answers the question shown and goes on to the next one. A
// multi_select question with nothing checked takes the option under the
// cursor; Other is no answer while nothing is typed there.
func (f *form) enter() {
	q := f.qs[f.step]
	cur, other := f.cur[f.step], len(q.Options)
	typed := strings.TrimSpace(f.other[f.step]) != ""
	if q.MultiSelect {
		if !typed && !slices.Contains(f.picked[f.step], true) {
			if cur == other {
				return
			}
			f.picked[f.step][cur] = true
		}
	} else {
		if cur == other && !typed {
			return
		}
		f.chosen[f.step] = cur
	}
	if f.step == len(f.qs)-1 {
		f.done = true
		return
	}
	f.step++
}

// answers are the user's, once the form is answered; nil while it is not,
// or when it was cancelled.
func (f *form) answers() []agent.Answer {
	if !f.done || f.cancelled {
		return nil
	}
	out := make([]agent.Answer, len(f.qs))
	for i, q := range f.qs {
		typed := strings.TrimSpace(f.other[i])
		switch {
		case q.MultiSelect:
			for j, o := range q.Options {
				if f.picked[i][j] {
					out[i].Picked = append(out[i].Picked, o.Label)
				}
			}
			out[i].Other = typed
		case f.chosen[i] == len(q.Options):
			out[i].Other = typed
		default:
			out[i].Picked = []string{q.Options[f.chosen[i]].Label}
		}
	}
	return out
}
