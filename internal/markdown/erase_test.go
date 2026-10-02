package markdown

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// term is as much of a terminal as the Writer's output needs: rows that
// wrap at cols with xterm's pending wrap, "\n" as the PTY's onlcr sends
// it, cursor up and down, erase in line and below, styles ignored. Erase
// below from the top-left corner is what tmux takes for clearing the
// screen: with scroll-on-clear, on by default, it first moves the screen
// to its history.
type term struct {
	cols, x, y int
	wrap       bool
	rows       [][]rune
	history    []string
}

func newTerm(cols, height int) *term {
	s := &term{cols: cols, rows: make([][]rune, height)}
	for i := range s.rows {
		s.rows[i] = s.blank()
	}
	return s
}

func (s *term) blank() []rune { return []rune(strings.Repeat(" ", s.cols)) }

func (s *term) down() {
	if s.y < len(s.rows)-1 {
		s.y++
		return
	}
	s.history = append(s.history, strings.TrimRight(string(s.rows[0]), " "))
	s.rows = append(s.rows[1:], s.blank())
}

func (s *term) write(t *testing.T, out string) {
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
				t.Fatalf("an escape the terminal does not know in %q", out)
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

func (s *term) csi(t *testing.T, param string, final rune) {
	t.Helper()
	if final == 'm' {
		return
	}
	n := 1
	if param != "" {
		v, err := strconv.Atoi(param)
		if err != nil {
			t.Fatalf("CSI %s%c: %v", param, final, err)
		}
		n = v
	}
	switch final {
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
		t.Fatalf("CSI %s%c the terminal does not know", param, final)
	}
}

func (s *term) lines() []string {
	var out []string
	for _, row := range s.rows {
		out = append(out, strings.TrimRight(string(row), " "))
	}
	return out
}

// TestEraseRaw streams a paragraph of every length up to three rows, at
// the top of the screen, below a line of output and at the bottom of a
// full screen, whole and in chunks: once the line is complete, its raw
// preview gives way to the rendering, and the screen, tmux's history and
// the cursor are as if the rendering had been written alone. Erase below
// never comes right after the cursor went up to the line's first row,
// which may be the top one.
func TestEraseRaw(t *testing.T) {
	const cols, height = 20, 6
	words := strings.Repeat("lorem ipsum dolor sit amet ", 4)
	befores := map[string]string{
		"top":    "",
		"below":  "output\n",
		"bottom": strings.Repeat("output\n", height),
	}
	failed := map[string]bool{}
	for n := 1; n <= 3*cols; n++ {
		line := words[:n]
		for where, before := range befores {
			for _, chunk := range []int{1, 7, 0} {
				key := fmt.Sprintf("%s, chunks of %d", where, chunk)
				if chunk == 0 {
					key, chunk = where+", whole", n
				}
				if failed[key] {
					continue // one length is enough to tell
				}
				var out strings.Builder
				m := New(&out, func() (int, int) { return cols, height }, "monokai")
				for i := 0; i < n; i += chunk {
					m.Write([]byte(line[i:min(i+chunk, n)]))
				}
				m.Write([]byte("\n"))
				m.Flush()

				var whole strings.Builder
				m = New(&whole, func() (int, int) { return cols, height }, "monokai")
				m.Write([]byte(line + "\n"))
				m.Flush()

				got, want := newTerm(cols, height), newTerm(cols, height)
				got.write(t, before)
				got.write(t, out.String())
				want.write(t, before)
				want.write(t, whole.String())
				bad := strings.Count(out.String(), "\x1b[J") != strings.Count(out.String(), "\x1b[B\x1b[J")
				if bad || strings.Join(got.lines(), "\n") != strings.Join(want.lines(), "\n") ||
					strings.Join(got.history, "\n") != strings.Join(want.history, "\n") || got.x != want.x || got.y != want.y {
					failed[key] = true
					t.Errorf("%s, %d characters:\nscreen  %q\nwant    %q\nhistory %q\nwant    %q\ncursor at %d,%d, want %d,%d\noutput %q",
						key, n, got.lines(), want.lines(), got.history, want.history, got.x, got.y, want.x, want.y, out.String())
				}
			}
		}
	}
}
