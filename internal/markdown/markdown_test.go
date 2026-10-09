package markdown

import (
	"regexp"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
	"strings"
	"testing"
)

var escRe = regexp.MustCompile("\x1b\\[[0-9;?]*[A-Za-z]|\x1b\\]8;;[^\x07]*\x07")

// render feeds s in chunks of n bytes and returns the final screen text
// with styles removed.
func render(s string, n int) string {
	var b strings.Builder
	m := New(&b, func() (int, int) { return 40, 24 }, "monokai")
	for i := 0; i < len(s); i += n {
		m.Write([]byte(s[i:min(i+n, len(s))]))
	}
	m.Flush()
	return b.String()
}

// screen replays the output on a 40-column terminal: a line shown raw and
// then rendered keeps only the rendering.
func screen(out string) string {
	var lines []string
	cur := ""
	for len(out) > 0 {
		if runewidth.StringWidth(cur) == 40 && out[0] != '\n' && out[0] != '\r' && out[0] != '\x1b' {
			lines = append(lines, cur)
			cur = ""
		}
		if loc := escRe.FindStringIndex(out); loc != nil && loc[0] == 0 {
			seq := out[:loc[1]]
			out = out[loc[1]:]
			if strings.HasSuffix(seq, "A") {
				k := 0
				for _, c := range seq[2 : len(seq)-1] {
					k = k*10 + int(c-'0')
				}
				k = max(k, 1)
				cur = lines[len(lines)-k]
				lines = lines[:len(lines)-k]
			}
			if seq == "\x1b[B" { // going up has dropped the rows below
				lines = append(lines, cur)
				cur = ""
			}
			if seq == "\x1b[J" || seq == "\x1b[K" { // erased from the start of the row
				cur = ""
			}
			continue
		}
		r, size := utf8.DecodeRuneInString(out)
		out = out[size:]
		switch r {
		case '\r':
			cur = ""
		case '\n':
			lines = append(lines, cur)
			cur = ""
		default:
			cur += string(r)
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return strings.Join(lines, "\n")
}

func TestRender(t *testing.T) {
	in := "# Title\n\nSome **bold** and `code` and [link](http://x).\n\n- one\n  - two\n- [x] done\n1. first\n\n> quoted\n\n---\n\n```go\nfunc main() {}\n```\n\n| a | b |\n|---|--:|\n| 1 | 22 |\n\nend"
	want := "Title\n\nSome bold and code and link (http://x).\n\n• one\n  ◦ two\n• ☑ done\n1. first\n\n│ quoted\n\n────────────────────────────────────────\n\nfunc main() {}\n\n┌───┬────┐\n│ a │  b │\n├───┼────┤\n│ 1 │ 22 │\n└───┴────┘\n\nend"
	for _, n := range []int{1, 3, 7, 1000} {
		if got := screen(render(in, n)); got != want {
			t.Errorf("chunk %d:\n%s\nwant:\n%s", n, got, want)
		}
	}
}

func TestWrap(t *testing.T) {
	got := screen(render("- aaaa bbbb cccc dddd eeee ffff gggg hhhh iiii\n", 1000))
	want := "• aaaa bbbb cccc dddd eeee ffff gggg\n  hhhh iiii"
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

func TestInline(t *testing.T) {
	for in, want := range map[string]string{
		"snake_case_name": "snake_case_name",
		"2 * 3 * 4":       "2 * 3 * 4",
		`\*not\*`:         "*not*",
		"~~gone~~ _it_":   "gone it",
		"``a `b` c``":     "a `b` c",
		"unclosed **bold": "unclosed **bold",
	} {
		if got := escRe.ReplaceAllString(inline(in, ""), ""); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

func TestHyperlink(t *testing.T) {
	open := func(url string) string { return "\x1b]8;;" + url + "\a" }
	const end = "\x1b]8;;\a"
	for in, want := range map[string]string{
		"[docs](https://x.dev/a)":  open("https://x.dev/a") + "\x1b[34mdocs\x1b[0m" + end,
		"<https://x.dev/b>":        open("https://x.dev/b") + "https://x.dev/b" + end,
		"[rel](foo.go)":            "foo.go",
		"[bad](https://x\x1b[31m)": "https://x\x1b[31m",
	} {
		got := inline(in, "")
		if !strings.Contains(got, want) {
			t.Errorf("%q: got %q, want it to contain %q", in, got, want)
		}
	}
	if got := width(inline("[docs](https://x.dev/a)", "")); got != len("docs (https://x.dev/a)") {
		t.Errorf("width %d counts the hyperlink escapes", got)
	}
}

func TestNarrowTable(t *testing.T) {
	got := screen(render("| name | description |\n|---|---|\n| x | a rather long description that wraps |\n", 1000))
	want := "┌──────┬───────────────────────────────┐\n│ name │ description                   │\n├──────┼───────────────────────────────┤\n│ x    │ a rather long description     │\n│      │ that wraps                    │\n└──────┴───────────────────────────────┘"
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}
