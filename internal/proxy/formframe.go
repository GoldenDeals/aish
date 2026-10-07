package proxy

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
)

// frame is what the screen shows, lines separated by "\r\n": the question
// being answered, or, once the form is answered, the summary of the
// answers, which stays on the screen; nothing when it was cancelled. A
// question fits in cols-1 columns and rows-1 lines (rows 0: the height is
// not known): a line that wrapped, or a frame that scrolled the screen,
// would throw off the redraw, which goes back as many lines as it drew.
func (f *form) frame(cols, rows int) string {
	if f.done {
		return f.summary()
	}
	w := max(cols-1, 1)
	q := f.qs[f.step]
	question := wrap(q.Question, w-2)
	opts := make([][]string, len(q.Options)+1) // Other last
	for j := range opts {
		opts[j] = f.option(j, w)
	}
	lo, hi := 0, len(opts)-1
	if rows > 0 {
		question, lo, hi = fitRows(question, opts, f.cur[f.step], rows-3) // the header and the hint
	}

	var lines []string
	add := func(segs ...seg) { lines = append(lines, fit(w, segs...)) }
	add(seg{bold, "  " + oneLine(q.Name())}, seg{dim, fmt.Sprintf(" %d/%d", f.step+1, len(f.qs))})
	for _, l := range question {
		add(seg{"", "  " + l})
	}
	if lo > 0 {
		add(seg{dim, fmt.Sprintf("  ↑ %d more", lo)})
	}
	for _, o := range opts[lo : hi+1] {
		lines = append(lines, o...)
	}
	if n := len(opts) - 1 - hi; n > 0 {
		add(seg{dim, fmt.Sprintf("  ↓ %d more", n)})
	}
	add(seg{dim, "  " + f.hint()})
	return strings.Join(lines, "\r\n")
}

// option is the lines of the option j of the question shown, Other being
// the last one: its label and its description.
func (f *form) option(j, w int) []string {
	q := f.qs[f.step]
	other := len(q.Options)
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
		lines := []string{fit(w, seg{style, lead + oneLine(q.Options[j].Label)})}
		if d := q.Options[j].Description; strings.TrimSpace(d) != "" {
			for _, l := range wrap(d, w-len(indent)) {
				lines = append(lines, fit(w, seg{dim, indent + l}))
			}
		}
		return lines
	}
	typed, editing := f.other[f.step], j == f.cur[f.step]
	if typed == "" && !editing {
		return []string{fit(w, seg{style, lead + "Other"})}
	}
	lead += "Other: "
	room := w - runewidth.StringWidth(lead) - 1 // the cursor
	segs := []seg{{style, lead}, {"", tailFit(oneLine(typed), room)}}
	if editing {
		segs = append(segs, seg{reverse, " "})
	}
	return []string{fit(w, segs...)}
}

// fitRows makes the question and the options, each its lines, fit in room
// lines, as far as they can: it drops the descriptions but the one under
// the cursor, then shows the options around the cursor alone, with a line
// for those above and one for those below, then cuts the question.
// Returns the lines of the question left and the options shown, lo to hi;
// opts loses the lines dropped.
func fitRows(question []string, opts [][]string, cur, room int) ([]string, int, int) {
	last := len(opts) - 1
	size := func(lo, hi int) int {
		n := 0
		if lo > 0 {
			n++
		}
		if hi < last {
			n++
		}
		for _, o := range opts[lo : hi+1] {
			n += len(o)
		}
		return n
	}
	if len(question)+size(0, last) <= room {
		return question, 0, last
	}
	for j := range opts {
		if j != cur {
			opts[j] = opts[j][:1]
		}
	}
	if len(question)+size(0, last) <= room {
		return question, 0, last
	}
	// Room for the option under the cursor and the lines above and below
	// it, wherever it is: the question stays as it is while the cursor
	// moves.
	if n := max(room-min(3, len(opts)), 0); n < len(question) {
		question = question[:n]
		if n > 0 {
			question[n-1] += "…"
		}
	}
	avail := room - len(question)
	if size(cur, cur) > avail {
		opts[cur] = opts[cur][:1]
	}
	lo, hi := cur, cur
	for grown := true; grown; {
		grown = false
		if hi < last && size(lo, hi+1) <= avail {
			hi, grown = hi+1, true
		}
		if lo > 0 && size(lo-1, hi) <= avail {
			lo, grown = lo-1, true
		}
	}
	return question, lo, hi
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
