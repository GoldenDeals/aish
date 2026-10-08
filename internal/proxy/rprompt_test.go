package proxy

import (
	"strconv"
	"strings"
	"testing"
)

// zshPrompt is what zsh 5.9 prints after cmd-end for PS1='%# ' and
// RPROMPT='[%?]' 40 columns wide: the prompt, then [0] at its right.
const zshPrompt = "\r\x1b[0m\x1b[27m\x1b[24m\x1b[J% \x1b[K\x1b[34C[0]\x1b[37D\x1b[?2004h"

// row0 is the first row of a terminal cols wide after out: the little of a
// terminal the status and zle use.
func row0(t *testing.T, cols int, out string) string {
	t.Helper()
	line := []rune(strings.Repeat(" ", cols))
	row, col, saved := 0, 0, [2]int{}
	for i := 0; i < len(out); {
		c := out[i]
		switch {
		case c == '\r':
			col = 0
			i++
		case c == '\b':
			col = max(col-1, 0)
			i++
		case c == 0x1b && i+1 < len(out) && out[i+1] == '7':
			saved = [2]int{row, col}
			i += 2
		case c == 0x1b && i+1 < len(out) && out[i+1] == '8':
			row, col = saved[0], saved[1]
			i += 2
		case c == 0x1b && i+1 < len(out) && out[i+1] == '[':
			j := i + 2
			for j < len(out) && (out[j] >= '0' && out[j] <= '9' || out[j] == ';' || out[j] == '?') {
				j++
			}
			params := out[i+2 : j]
			n, err := strconv.Atoi(params)
			if err != nil {
				n = 0
			}
			switch out[j] {
			case 'A':
				row -= max(n, 1)
			case 'B':
				row += max(n, 1)
			case 'C':
				col = min(col+max(n, 1), cols-1)
			case 'D':
				col = max(col-max(n, 1), 0)
			case 'G':
				col = max(n, 1) - 1
			case 'K', 'J':
				if row == 0 && n == 0 {
					for k := col; k < cols; k++ {
						line[k] = ' '
					}
				}
			case 'X':
				if row == 0 {
					for k := col; k < min(col+max(n, 1), cols); k++ {
						line[k] = ' '
					}
				}
			case 'm', 'h', 'l':
			default:
				t.Fatalf("row0: no %q in %q", out[i:j+1], out)
			}
			i = j + 1
		default:
			if row == 0 && col < cols {
				line[col] = rune(c)
			}
			col++
			i++
		}
	}
	return strings.TrimRight(string(line), " ")
}

// at is a row with each text at its column.
func at(texts ...any) string {
	var b []rune
	for i := 0; i+1 < len(texts); i += 2 {
		col, s := texts[i].(int), texts[i+1].(string)
		for len(b) < col {
			b = append(b, ' ')
		}
		b = append(b[:col], []rune(s)...)
	}
	return string(b)
}

// TestInputLineRPrompt: zsh's RPROMPT is where the status would be. The
// status goes left of it, a column between them, goes with the first key
// that puts text on the line, and stays off where the line has no room
// for it. Neither covers the other.
func TestInputLineRPrompt(t *testing.T) {
	for _, tc := range []struct {
		name, prompt string
		keys         []string // zle's output for each key
		want         []string // row 0 after the prompt and after each key
	}{
		{
			name:   "status left of RPROMPT",
			prompt: zshPrompt,
			keys:   []string{"a", "\b \b"},
			want:   []string{at(0, "%", 30, "ctx-m", 36, "[0]"), at(0, "% a", 36, "[0]"), at(0, "%", 36, "[0]")},
		},
		{
			name:   "no room left of it",
			prompt: "\r\x1b[J" + strings.Repeat("x", 28) + "% \x1b[K\x1b[6C[0]\x1b[9D",
			want:   []string{at(0, strings.Repeat("x", 28)+"%", 36, "[0]")},
		},
		{
			// zle's RPROMPT gone, the status is back at the edge.
			name:   "RPROMPT erased",
			prompt: zshPrompt,
			keys:   []string{"\x1b[K"},
			want:   []string{at(0, "%", 30, "ctx-m", 36, "[0]"), at(0, "%", 34, "ctx-m")},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := newInputLine(40, 24, "ctx-m", "")
			screen := string(l.draw()) + string(l.feed([]byte(tc.prompt)))
			if got := row0(t, 40, screen); got != tc.want[0] {
				t.Errorf("prompt:\n got %q\nwant %q", got, tc.want[0])
			}
			for i, k := range tc.keys {
				l.typed()
				screen += string(l.feed([]byte(k)))
				if got := row0(t, 40, screen); got != tc.want[i+1] {
					t.Errorf("key %d %q:\n got %q\nwant %q", i, k, got, tc.want[i+1])
				}
			}
		})
	}
}
