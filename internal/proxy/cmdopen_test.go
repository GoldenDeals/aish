package proxy

import (
	"slices"
	"strings"
	"testing"
)

// Without folding (fold_lines = -1) a command of the agent passes through:
// agent-end ends the last line it left open, so the spinner of the next
// turn starts below it, and cmd-end does so for a command cut short by
// Ctrl+C, so the prompt does. A line already ended gets no empty line.
func TestCommandUnfoldedOpenLine(t *testing.T) {
	ends := []struct {
		name string
		mark Marker
		next func(p *Proxy) // what follows on the screen
		row  string         // and the row it leaves
	}{
		{"agent-end", Marker{Kind: "agent-end", Payload: "c1;0;/tmp"},
			func(p *Proxy) { (&ui{p: p}).Write([]byte(spinnerFrame)) }, "⠋ thinking…"},
		{"cmd-end", Marker{Kind: "cmd-end", Payload: "130;/tmp"},
			func(p *Proxy) { p.output([]byte("$ ")) }, "$"},
	}
	for _, tc := range []struct {
		name   string
		out    []string
		end    string // what the marker prints
		screen []string
	}{
		{"open", []string{"a\r\nb"}, "\r\n", []string{"a", "b"}},
		{"ended", []string{"a\r\nb\r\n"}, "", []string{"a", "b"}},
		{"open at last write", []string{"a\r\n", "b"}, "\r\n", []string{"a", "b"}},
		{"colours reset after the end", []string{"a\r\nb\r\n\x1b[m"}, "", []string{"a", "b"}},
		{"line erased", []string{"a\r\n50%\r\x1b[K"}, "", []string{"a"}},
		{"no output", nil, "", nil},
	} {
		for _, e := range ends {
			p, out := statusProxy(t, 80)
			p.foldLines = -1
			p.marker(Marker{Kind: "agent-start", Payload: "c1;printf 'a\\nb'"})
			for _, w := range tc.out {
				p.output([]byte(w))
			}
			before := out.String()
			p.marker(e.mark)
			if end := strings.TrimPrefix(out.String(), before); end != tc.end {
				t.Errorf("%s, %s: printed %q, want %q", tc.name, e.name, end, tc.end)
			}
			e.next(p)
			want := append(slices.Clone(tc.screen), e.row)
			if got := screenRows(out.String()); !slices.Equal(got, want) {
				t.Errorf("%s, %s: screen %q, want %q", tc.name, e.name, got, want)
			}
		}
	}
}
