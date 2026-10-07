package shellinit

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// screen is as much of a terminal as __aish_unecho's output and readline's
// echo need: rows that wrap at cols with xterm's pending wrap, a wide
// character that does not fit in what is left of a row put on the next,
// "\n" as the PTY's onlcr sends it, cursor up, down and home, erase in
// line, below and all. Erase all, and erase below from the top-left
// corner, is what tmux takes for clearing the screen: with
// scroll-on-clear, on by default, it first moves the screen to its
// history.
type screen struct {
	cols, x, y int
	wrap       bool
	rows       [][]rune
	history    []string
}

// wideCont fills the cell a wide character takes after its own.
const wideCont = '\x00'

// wideRanges are the East Asian wide and fullwidth characters and emoji
// __aish_rows knows.
var wideRanges = [][2]rune{
	{0x1100, 0x115f}, {0x2e80, 0xa4cf}, {0xac00, 0xd7a3}, {0xf900, 0xfaff}, {0xfe30, 0xfe4f},
	{0xff00, 0xff60}, {0xffe0, 0xffe6}, {0x1f300, 0x1faff}, {0x20000, 0x3fffd},
}

// runeCols is the columns a terminal gives r: two for a wide character,
// one for the rest.
func runeCols(r rune) int {
	for _, w := range wideRanges {
		if w[0] <= r && r <= w[1] {
			return 2
		}
	}
	return 1
}

// rowText is a row as it reads, without the cells wide characters fill.
func rowText(row []rune) string {
	return strings.TrimRight(strings.ReplaceAll(string(row), string(wideCont), ""), " ")
}

func newScreen(cols, height int) *screen {
	s := &screen{cols: cols, rows: make([][]rune, height)}
	for i := range s.rows {
		s.rows[i] = s.blank()
	}
	return s
}

func (s *screen) blank() []rune { return []rune(strings.Repeat(" ", s.cols)) }

func (s *screen) down() {
	if s.y < len(s.rows)-1 {
		s.y++
		return
	}
	s.history = append(s.history, rowText(s.rows[0]))
	s.rows = append(s.rows[1:], s.blank())
}

func (s *screen) write(t *testing.T, out string) {
	t.Helper()
	rs := []rune(out)
	for i := 0; i < len(rs); i++ {
		switch r := rs[i]; r {
		case '\r':
			s.x, s.wrap = 0, false
		case '\n':
			s.x, s.wrap = 0, false
			s.down()
		case '\x1b':
			j := i + 1
			if j >= len(rs) || rs[j] != '[' {
				t.Fatalf("an escape the screen does not know in %q", out)
			}
			for j++; j < len(rs) && (rs[j] < 0x40 || rs[j] > 0x7e); j++ {
			}
			if j == len(rs) {
				t.Fatalf("a cut escape in %q", out)
			}
			s.csi(t, string(rs[i+2:j]), rs[j])
			i = j
		default:
			w := runeCols(r)
			if s.wrap || s.x+w > s.cols {
				s.x, s.wrap = 0, false
				s.down()
			}
			s.rows[s.y][s.x] = r
			if w == 2 {
				s.rows[s.y][s.x+1] = wideCont
			}
			if s.x+w == s.cols {
				s.x, s.wrap = s.cols-1, true
			} else {
				s.x += w
			}
		}
	}
}

func (s *screen) csi(t *testing.T, param string, final rune) {
	t.Helper()
	n := 1
	if param != "" && final != 'm' {
		v, err := strconv.Atoi(param)
		if err != nil {
			t.Fatalf("CSI %s%c: %v", param, final, err)
		}
		n = v
	}
	switch final {
	case 'm':
	case 'A':
		s.y, s.wrap = max(s.y-n, 0), false
	case 'B':
		s.y, s.wrap = min(s.y+n, len(s.rows)-1), false
	case 'H':
		if param != "" {
			t.Fatalf("CSI %sH", param)
		}
		s.x, s.y, s.wrap = 0, 0, false
	case 'K':
		if param != "" {
			t.Fatalf("CSI %sK", param)
		}
		copy(s.rows[s.y][s.x:], s.blank())
	case 'J':
		if param != "" && param != "2" {
			t.Fatalf("CSI %sJ", param)
		}
		if param == "2" || s.x == 0 && s.y == 0 {
			last := 0
			for i, row := range s.rows {
				if strings.TrimSpace(string(row)) != "" {
					last = i + 1
				}
			}
			for _, row := range s.rows[:last] {
				s.history = append(s.history, rowText(row))
			}
		}
		if param == "2" {
			for _, row := range s.rows {
				copy(row, s.blank())
			}
			break
		}
		copy(s.rows[s.y][s.x:], s.blank())
		for _, row := range s.rows[s.y+1:] {
			copy(row, s.blank())
		}
	default:
		t.Fatalf("CSI %s%c the screen does not know", param, final)
	}
}

// lines are the rows down to the last one with text.
func (s *screen) lines() []string {
	var out []string
	for _, row := range s.rows {
		out = append(out, rowText(row))
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// rowsOf is text as a terminal cols wide shows it, a row a line.
func rowsOf(text string, cols int) []string {
	var out []string
	var row []rune
	x := 0
	for _, r := range text {
		if x+runeCols(r) > cols {
			out = append(out, strings.TrimRight(string(row), " "))
			row, x = nil, 0
		}
		row = append(row, r)
		x += runeCols(r)
	}
	return append(out, strings.TrimRight(string(row), " "))
}

// TestUnecho has __aish_unecho put the request in place of readline's echo
// of `__aish_ask "$__aish_req"`, at two widths, after a prompt of every
// length up to two rows and more, its directory of narrow characters or of
// wide ones, which go to the next row when one column is left, with an
// emoji or without, the echo at the top of the screen or below a line of
// output: the line stays, the echo is gone, from the screen and from tmux's
// history, and the cursor is under the request. Readline leaves the cursor
// under the echo's last row, a full one too, as "\n" after a pending wrap
// does.
func TestUnecho(t *testing.T) {
	const req = "Что здесь происходит?"
	// {} is the directory, as \w puts it in the prompt.
	prompts := []struct{ ps1, shown, asked string }{
		{`\[\e[32m\]user@host\[\e[0m\]:~/{}$ `, "user@host:~/{}$ ", "user@host:~/{}? "},
		{`{}> `, "{}> ", "{}> ? "},
		{`🚀 \[\e[32m\]user@host\[\e[0m\]:~/{}$ `, "🚀 user@host:~/{}$ ", "🚀 user@host:~/{}? "},
	}
	type unecho struct {
		cols               int
		prompt, words, dir string
		shown, asked       string
	}
	var cases []unecho
	var script strings.Builder
	for _, p := range prompts {
		for _, words := range []string{"Задай мне вопрос ", "漢字で質問 "} {
			rs := []rune(strings.Repeat(words, 40))
			for _, cols := range []int{40, 80} {
				// w is the columns of rs[:n].
				for n, w := 0, 0; w <= 2*cols+2; n, w = n+1, w+runeCols(rs[n]) {
					dir := strings.NewReplacer("{}", string(rs[:n]))
					cases = append(cases, unecho{cols, p.shown, words, string(rs[:n]), dir.Replace(p.shown), dir.Replace(p.asked)})
					fmt.Fprintf(&script, "PS1=%s; COLUMNS=%d; __aish_unecho %s; printf '\\x1f'\n", quote(dir.Replace(p.ps1)), cols, quote(req))
				}
			}
		}
	}
	out := routed(t, "", "", script.String())
	if len(out) < len(cases) {
		t.Fatalf("%d outputs for %d requests", len(out), len(cases))
	}
	failed := map[string]bool{}
	for i, c := range cases {
		echo := c.shown + `__aish_ask "$__aish_req"`
		for _, above := range []string{"", "above"} {
			key := fmt.Sprintf("%q, %q, %d columns, below %q", c.prompt, c.words, c.cols, above)
			if failed[key] {
				continue // one length is enough to tell
			}
			s := newScreen(c.cols, 12)
			var want []string
			if above != "" {
				s.write(t, above+"\n")
				want = append(want, above)
			}
			s.write(t, echo+"\n")
			s.write(t, out[i])
			want = append(want, rowsOf(c.asked+req, c.cols)...)
			got := s.lines()
			if strings.Join(got, "\n") != strings.Join(want, "\n") || len(s.history) > 0 || s.x != 0 || s.y != len(want) {
				failed[key] = true
				t.Errorf("%s, a directory of %d characters:\nscreen %q\nwant   %q\nhistory %q\ncursor at %d,%d, want 0,%d\noutput %q",
					key, len([]rune(c.dir)), got, want, s.history, s.x, s.y, len(want), out[i])
			}
		}
	}
}

// TestRows has __aish_rows take both ends of each range of wide characters
// for two columns and the characters next to them for one: after a column
// of a row two columns wide, a wide character goes to the next row.
func TestRows(t *testing.T) {
	var rs []rune
	for _, w := range wideRanges {
		rs = append(rs, w[0]-1, w[0], w[1], w[1]+1)
	}
	var script strings.Builder
	for _, r := range rs {
		fmt.Fprintf(&script, "__aish_rows %s 2; printf '%%s\\x1f' \"$__aish_n\"\n", quote("a"+string(r)))
	}
	out := routed(t, "", "", script.String())
	if len(out) < len(rs) {
		t.Fatalf("output %q", out)
	}
	for i, r := range rs {
		if want := strconv.Itoa(runeCols(r)); out[i] != want {
			t.Errorf("U+%04X: %q rows, want %s", r, out[i], want)
		}
	}
}
