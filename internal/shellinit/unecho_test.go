package shellinit

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// screen is as much of a terminal as __aish_unecho's output needs: rows
// that wrap at cols with xterm's pending wrap, "\n" as the PTY's onlcr
// sends it, cursor up and down, erase in line and below. Erase below from
// the top-left corner is what tmux takes for clearing the screen: with
// scroll-on-clear, on by default, it first moves the screen to its
// history.
type screen struct {
	cols, x, y int
	wrap       bool
	rows       [][]rune
	history    []string
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
	s.history = append(s.history, strings.TrimRight(string(s.rows[0]), " "))
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
			if s.wrap {
				s.x, s.wrap = 0, false
				s.down()
			}
			s.rows[s.y][s.x] = r
			if s.x == s.cols-1 {
				s.wrap = true
			} else {
				s.x++
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
	case 'K':
		if param != "" {
			t.Fatalf("CSI %sK", param)
		}
		copy(s.rows[s.y][s.x:], s.blank())
	case 'J':
		if param != "" {
			t.Fatalf("CSI %sJ", param)
		}
		if s.x == 0 && s.y == 0 {
			last := 0
			for i, row := range s.rows {
				if strings.TrimSpace(string(row)) != "" {
					last = i + 1
				}
			}
			for _, row := range s.rows[:last] {
				s.history = append(s.history, strings.TrimRight(string(row), " "))
			}
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
		out = append(out, strings.TrimRight(string(row), " "))
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// rowsOf is text as a terminal cols wide shows it, a row a line.
func rowsOf(text string, cols int) []string {
	rs := []rune(text)
	var out []string
	for len(rs) > cols {
		out = append(out, strings.TrimRight(string(rs[:cols]), " "))
		rs = rs[cols:]
	}
	return append(out, strings.TrimRight(string(rs), " "))
}

// TestUnecho has __aish_unecho put the request in place of readline's echo
// of `__aish_ask '...'`, at two widths and every length of the request up
// to two rows and more, the echo at the top of the screen or below a line
// of output: the line stays, the echo is gone, from the screen and from
// tmux's history, and the cursor is under the request. Readline leaves the
// cursor under the echo's last row, a full one too, as "\n" after a pending
// wrap does.
func TestUnecho(t *testing.T) {
	prompts := []struct{ ps1, shown, asked string }{
		{`\[\e[32m\]user@host\[\e[0m\]:~$ `, "user@host:~$ ", "user@host:~? "},
		{`> `, "> ", "> ? "},
	}
	words := []rune(strings.Repeat("Задай мне вопрос ", 20))
	type unecho struct {
		cols         int
		shown, asked string
		req          string
	}
	var cases []unecho
	var script strings.Builder
	for _, p := range prompts {
		for _, cols := range []int{40, 80} {
			for n := 1; n <= 2*cols+2; n++ {
				req := string(words[:n])
				cases = append(cases, unecho{cols, p.shown, p.asked, req})
				fmt.Fprintf(&script, "PS1=%s; COLUMNS=%d; __aish_unecho %s; printf '\\x1f'\n", quote(p.ps1), cols, quote(req))
			}
		}
	}
	out := routed(t, "", "", script.String())
	if len(out) < len(cases) {
		t.Fatalf("%d outputs for %d requests", len(out), len(cases))
	}
	failed := map[string]bool{}
	for i, c := range cases {
		echo := c.shown + "__aish_ask " + quote(c.req)
		for _, above := range []string{"", "above"} {
			key := fmt.Sprintf("%q, %d columns, below %q", c.shown, c.cols, above)
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
			want = append(want, rowsOf(c.asked+c.req, c.cols)...)
			y := len(want)
			for want[len(want)-1] == "" { // a space wrapped to a row of its own
				want = want[:len(want)-1]
			}
			got := s.lines()
			if strings.Join(got, "\n") != strings.Join(want, "\n") || len(s.history) > 0 || s.x != 0 || s.y != y {
				failed[key] = true
				t.Errorf("%s, a request of %d characters:\nscreen %q\nwant   %q\nhistory %q\ncursor at %d,%d, want 0,%d\noutput %q",
					key, len([]rune(c.req)), got, want, s.history, s.x, s.y, y, out[i])
			}
		}
	}
}
