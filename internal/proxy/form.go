package proxy

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"

	"github.com/inebotov/aish/internal/agent"
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

// feed takes what the user typed and reports whether the form is over:
// answered or cancelled. Keys after that are not the form's.
func (f *form) feed(b []byte) (done bool) {
	for len(b) > 0 && !f.done {
		n, k, r := nextKey(b)
		b = b[n:]
		f.press(k, r)
	}
	return f.done
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

// frame is what the screen shows, lines separated by "\r\n": the question
// being answered, or, once the form is answered, the summary of the
// answers, which stays on the screen; nothing when it was cancelled. Every
// line of a question fits in cols-1 columns: one that wrapped would throw
// off the redraw, which goes back as many lines as it drew.
func (f *form) frame(cols int) string {
	if f.done {
		return f.summary()
	}
	w := max(cols-1, 1)
	q := f.qs[f.step]
	other := len(q.Options)
	var lines []string
	add := func(segs ...seg) { lines = append(lines, fit(w, segs...)) }

	add(seg{bold, "  " + oneLine(q.Name())}, seg{dim, fmt.Sprintf(" %d/%d", f.step+1, len(f.qs))})
	for _, l := range wrap(q.Question, w-2) {
		add(seg{"", "  " + l})
	}
	for j := 0; j <= other; j++ {
		mark, style := "    ", ""
		if j == f.cur[f.step] {
			mark, style = "  ❯ ", cyan
		}
		lead := fmt.Sprintf("%s%d. ", mark, j+1)
		if q.MultiSelect {
			box := "[ ] "
			if j < other && f.picked[f.step][j] || j == other && strings.TrimSpace(f.other[f.step]) != "" {
				box = "[x] "
			}
			lead += box
		}
		indent := strings.Repeat(" ", runewidth.StringWidth(lead))
		if j < other {
			add(seg{style, lead + oneLine(q.Options[j].Label)})
			if d := q.Options[j].Description; strings.TrimSpace(d) != "" {
				for _, l := range wrap(d, w-len(indent)) {
					add(seg{dim, indent + l})
				}
			}
			continue
		}
		typed, editing := f.other[f.step], j == f.cur[f.step]
		if typed == "" && !editing {
			add(seg{style, lead + "Other"})
			continue
		}
		lead += "Other: "
		room := w - runewidth.StringWidth(lead) - 1 // the cursor
		segs := []seg{{style, lead}, {"", tailFit(oneLine(typed), room)}}
		if editing {
			segs = append(segs, seg{reverse, " "})
		}
		add(segs...)
	}
	add(seg{dim, "  " + f.hint()})
	return strings.Join(lines, "\r\n")
}

func (f *form) hint() string {
	q := f.qs[f.step]
	n := len(q.Options) + 1
	var keys []string
	switch {
	case f.cur[f.step] == n-1:
		keys = append(keys, "type the answer", "↑↓ move")
	case q.MultiSelect:
		keys = append(keys, "↑↓ move", fmt.Sprintf("space/1-%d check", n))
	default:
		keys = append(keys, fmt.Sprintf("↑↓/1-%d choose", n))
	}
	if f.step == len(f.qs)-1 {
		keys = append(keys, "enter done")
	} else {
		keys = append(keys, "enter next")
	}
	if f.step > 0 {
		keys = append(keys, "← back")
	}
	return strings.Join(append(keys, "esc cancel"), " · ")
}

// summary is a line "header: answer" per question, as the model gets them.
func (f *form) summary() string {
	ans := f.answers()
	if ans == nil {
		return ""
	}
	lines := make([]string, len(f.qs))
	for i, q := range f.qs {
		lines[i] = dim + "  " + oneLine(q.Name()) + ":" + reset + " " + oneLine(ans[i].String())
	}
	return strings.Join(lines, "\r\n")
}

// seg is a piece of a line with its style.
type seg struct{ style, text string }

// fit draws segs in w columns at most, the text that does not fit cut
// with "…".
func fit(w int, segs ...seg) string {
	var b strings.Builder
	for _, s := range segs {
		t := s.text
		tw := runewidth.StringWidth(t)
		cut := tw > w
		if cut {
			t = runewidth.Truncate(t, w, "…")
			if tw = runewidth.StringWidth(t); tw > w {
				t, tw = "", 0
			}
		}
		if s.style != "" && t != "" {
			t = s.style + t + reset
		}
		b.WriteString(t)
		w -= tw
		if cut {
			break
		}
	}
	return b.String()
}

// wrap breaks s into lines of w columns at most, between words where it
// can.
func wrap(s string, w int) []string {
	w = max(w, 1)
	var out []string
	for _, para := range strings.Split(s, "\n") {
		line, lw := "", 0
		for _, word := range strings.Fields(oneLine(para)) {
			ww := runewidth.StringWidth(word)
			if lw > 0 && lw+1+ww <= w {
				line, lw = line+" "+word, lw+1+ww
				continue
			}
			if lw > 0 {
				out = append(out, line)
			}
			for ww > w {
				head := runewidth.Truncate(word, w, "")
				if head == "" { // a rune wider than the line
					_, size := utf8.DecodeRuneInString(word)
					head = word[:size]
				}
				out = append(out, head)
				word = word[len(head):]
				ww = runewidth.StringWidth(word)
			}
			line, lw = word, ww
		}
		out = append(out, line)
	}
	return out
}

// tailFit is the end of s that fits in w columns: what is being typed.
func tailFit(s string, w int) string {
	if runewidth.StringWidth(s) <= w {
		return s
	}
	rs := []rune(s)
	i, used := len(rs), runewidth.StringWidth("…")
	for i > 0 && used+runewidth.RuneWidth(rs[i-1]) <= w {
		used += runewidth.RuneWidth(rs[i-1])
		i--
	}
	return "…" + string(rs[i:])
}

// oneLine is s with control characters, which would move the cursor or
// take columns of their own, as spaces: the model wrote it.
func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

// frameWidth is how many columns a line of the frame takes: its text
// without the styles.
func frameWidth(line string) int {
	var b strings.Builder
	for i := 0; i < len(line); i++ {
		if line[i] == 0x1b && i+1 < len(line) && line[i+1] == '[' {
			for i += 2; i < len(line) && (line[i] < 0x40 || line[i] > 0x7e); i++ {
			}
			continue
		}
		b.WriteByte(line[i])
	}
	return runewidth.StringWidth(b.String())
}
