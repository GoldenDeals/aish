package proxy

import (
	"slices"
	"strings"
	"testing"
)

// spinnerFrame is how the agent's spinner starts its next turn: it takes
// the cursor for the start of a line and erases to its end.
const spinnerFrame = "\r⠋ thinking…\x1b[K"

// screenRows renders b as a terminal shows it, row by row: enough of one
// for \r, \n and erasing to the end of the line; other sequences print
// nothing.
func screenRows(b string) []string {
	rows := [][]rune{nil}
	row, col := 0, 0
	rs := []rune(b)
	for i := 0; i < len(rs); i++ {
		switch c := rs[i]; c {
		case '\r':
			col = 0
		case '\n':
			if row++; row == len(rows) {
				rows = append(rows, nil)
			}
		case 0x1b:
			if i+1 < len(rs) && rs[i+1] == ']' {
				for i < len(rs) && rs[i] != '\a' {
					i++
				}
				continue
			}
			if i+1 >= len(rs) || rs[i+1] != '[' {
				i++
				continue
			}
			j := i + 2
			for j < len(rs) && (rs[j] < 0x40 || rs[j] > 0x7e) {
				j++
			}
			if j < len(rs) && rs[j] == 'K' && j == i+2 && col < len(rows[row]) {
				rows[row] = rows[row][:col]
			}
			i = j
		default:
			if c < 0x20 {
				continue
			}
			for len(rows[row]) <= col {
				rows[row] = append(rows[row], ' ')
			}
			rows[row][col] = c
			col++
		}
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = strings.TrimRight(string(r), " ")
	}
	return out
}

// Output shown as it came may end inside a line: finish ends that line, so
// the spinner of the next turn starts below it. A line already ended, or
// one left with no text on it, gets no empty line after it.
func TestFoldFinishOpenLine(t *testing.T) {
	for _, tc := range []struct {
		name   string
		limit  int
		writes []string
		expand bool
		want   string
	}{
		{"shown, open", 5, []string{"a\nb"}, false, "\r\n"},
		{"shown, ended", 5, []string{"a\nb\n"}, false, ""},
		{"shown, open at last write", 5, []string{"a\n", "b"}, false, "\r\n"},
		{"colours reset after the end", 5, []string{"a\nb\n\x1b[m"}, false, ""},
		{"sequence split", 5, []string{"a\nb\n\x1b[", "?25h"}, false, ""},
		{"title set after the end", 5, []string{"a\n\x1b]0;b", "\a"}, false, ""},
		{"text after a sequence", 5, []string{"a\n\x1b[1mb\x1b[m"}, false, "\r\n"},
		{"carriage return keeps the text", 5, []string{"a\n50%\r"}, false, "\r\n"},
		{"line erased", 5, []string{"a\n50%\r\x1b[K"}, false, ""},
		{"line erased whole", 5, []string{"a\n50%\x1b[2K"}, false, ""},
		{"erased from the middle", 5, []string{"a\n50%\x1b[K"}, false, "\r\n"},
		{"full screen program", 5, []string{"a\n\x1b[?1049hvim\x1b[?1049l"}, false, ""},
		{"text after full screen", 5, []string{"\x1b[?1049hvim\x1b[?1049lb"}, false, "\r\n"},
		{"expanded while running", 0, []string{"1\n2"}, true, "\r\n"},
		{"expanded, ended", 0, []string{"1\n2\n"}, true, ""},
		{"no output", 5, nil, false, ""},
	} {
		f := newFold("⚙ probe", tc.limit)
		var shown strings.Builder
		for _, w := range tc.writes {
			shown.Write(f.write([]byte(w)))
		}
		if tc.expand {
			shown.Write(f.expand())
		}
		end := f.finish(-1)
		if string(end) != tc.want {
			t.Errorf("%s: finish %q, want %q", tc.name, end, tc.want)
			continue
		}
		if tc.want == "" {
			continue
		}
		rows := screenRows(shown.String() + string(end) + spinnerFrame)
		if last := rows[len(rows)-2]; last == "" {
			t.Errorf("%s: the last line of the output is gone: %q", tc.name, rows)
		}
	}

	// On the screen, the spinner of the next turn leaves b where it was.
	f := newFold("⚙ probe", 5)
	shown := string(f.write([]byte("a\r\nb"))) + string(f.finish(-1)) + spinnerFrame
	if got, want := screenRows(shown), []string{"a", "b", "⠋ thinking…"}; !slices.Equal(got, want) {
		t.Errorf("screen %q, want %q", got, want)
	}

	// Folded, the status ends the line as before: once, after the status.
	g := newFold("⚙ probe", 0)
	g.write([]byte("a\nb"))
	if got := string(g.finish(0)); !strings.HasSuffix(got, "(2 lines · ctrl+o to expand)"+reset+"\r\n") {
		t.Errorf("folded: %q", got)
	}
}

// Through the proxy: an external tool and a command of the agent shown
// below the call end their last line before the agent goes on.
func TestFinishOpenLineShown(t *testing.T) {
	p, out := statusProxy(t, 80)
	p.foldLines = 3
	u := &ui{p: p}
	u.CommandAt(9, false, 0)
	l := u.Live("⚙ probe")
	l.Write([]byte("a\nb"))
	l.Finish(0)
	u.Write([]byte(spinnerFrame))
	if got, want := screenRows(out.String()), []string{"", "a", "b", "⠋ thinking…"}; !slices.Equal(got, want) {
		t.Errorf("tool: screen %q, want %q", got, want)
	}

	p, out = statusProxy(t, 80)
	p.foldLines = 3
	p.marker(Marker{Kind: "agent-start", Payload: "c1;printf 'a\\nb'"})
	p.output([]byte("a\r\nb"))
	p.marker(Marker{Kind: "agent-end", Payload: "c1;0;/tmp"})
	(&ui{p: p}).Write([]byte(spinnerFrame))
	if got, want := screenRows(out.String()), []string{"a", "b", "⠋ thinking…"}; !slices.Equal(got, want) {
		t.Errorf("command: screen %q, want %q", got, want)
	}

	// An output that ended its line gets no empty line after it.
	p, out = statusProxy(t, 80)
	p.foldLines = 3
	p.marker(Marker{Kind: "agent-start", Payload: "c1;ls"})
	p.output([]byte("a\r\nb\r\n\x1b[m"))
	p.marker(Marker{Kind: "agent-end", Payload: "c1;0;/tmp"})
	if got := out.String(); got != "a\r\nb\r\n\x1b[m" {
		t.Errorf("ended: %q", got)
	}
}
