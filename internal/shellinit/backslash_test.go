package shellinit

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestExpandBackslashes checks the backslashes of an expanded request: they
// reach the model as typed, a backslash-newline and \\ too, but for \$, a
// dollar, as before. A run of them before a substitution keeps its parity:
// \$(...) does not run, \\$(...) does. In $(...) and ${...} they are shell
// syntax, as before. A line that is the here-document's delimiter once
// joined at a backslash-newline would end it early and run the lines after
// it: the text stays as typed, and nothing in it runs. One backslash at the
// end goes on to the next line (TestBackslashEnter).
func TestExpandBackslashes(t *testing.T) {
	const route = "capital=true\nnot_found=true\nsuffix=?\nmin_words=2\nexpand=true\n"
	cases := []struct{ in, want string }{
		{"Что делает $V:\nfind . \\\n-name a", "Что делает world:\nfind . \\\n-name a"},
		{`Say a \\ b $V`, `Say a \\ b world`},
		{`Cost \$5 and $V`, `Cost $5 and world`},
		{`Say $(printf 'a\\b')`, `Say a\b`},
		{`Say \\$V, \\\$V and \\\\$V`, `Say \\world, \\$V and \\\\world`},
		{`Say \$(echo hi), \\$(echo hi) and \\\$(echo hi) $V`, `Say $(echo hi), \\hi and \\$(echo hi) world`},
		{`Say $(echo hi) a\\b ${NOPE:-c\\d} $V`, `Say hi a\\b c\d world`},
		{`Say 'a\\b' \\ "c\\d" $V`, `Say 'a\\b' \\ "c\\d" world`},
		{"Say `ls` \\`x\\` \\\\`y\\\\` $V", "Say `ls` \\`x\\` \\\\`y\\\\` world"},
		{`Say \n, \t and \" to $V`, `Say \n, \t and \" to world`},
		{`Say $V \\`, `Say world \\`},
		{"Что делает $V?\nfor f in a b; do\n  cp -- \"$f\" /tmp \\\n    && printf '%s\\n' \"$f\" | sed 's/\\\\/\\//g'\ndone",
			"Что делает world?\nfor f in a b; do\n  cp -- \"\" /tmp \\\n    && printf '%s\\n' \"\" | sed 's/\\\\/\\//g'\ndone"},
		{"Say $V\n__aish_e\\\nof\n: >joined\nok", "Say $V\n__aish_e\\\nof\n: >joined\nok"},
		// After $[...] the walk stops, and the backslashes stay as they were.
		{"Say $[1] $V\n__aish_e\\\nof\n: >joined2\nok", "Say $[1] $V\n__aish_e\\\nof\n: >joined2\nok"},
	}
	var script strings.Builder
	for _, c := range cases {
		script.WriteString("__aish_fresh=1; READLINE_LINE=" + quote(c.in) + "; __aish_route; " + printLine +
			"; w=" + quote(c.want) + "; printf '__aish_ask %s\\x1f' \"${w@Q}\"\n")
	}
	// An escaped $(...) does not run, nor what follows a joined delimiter.
	for _, in := range []string{`Does \$(: >escaped) run, $V?`, `Does \\$(: >bare) run, $V?`} {
		script.WriteString("__aish_fresh=1; READLINE_LINE=" + quote(in) + "; __aish_route\n")
	}
	script.WriteString(`for f in escaped bare joined joined2; do [[ -e $f ]] && printf ' ran' || printf ' kept'; done; printf '\x1f'` + "\n")
	got := routed(t, route, "V=world", script.String())
	n := len(cases)
	if len(got) < 2*n+1 {
		t.Fatalf("output %q", got)
	}
	for i, c := range cases {
		if got[2*i] != got[2*i+1] {
			t.Errorf("%q: %q, want %q", c.in, got[2*i], got[2*i+1])
		}
	}
	if g, want := got[2*n], " kept ran kept kept"; g != want {
		t.Errorf(`\$(...), \\$(...), the lines after a joined delimiter: %q, want %q`, g, want)
	}
}

// TestBackslashEnter types requests ending in a backslash at a bash with
// the rc aish gives it. Enter goes on to the next line: readline keeps the
// line, with a newline for the backslash and the blanks before it, and the
// next Enter sends it whole, expanded then and only then, a line that is
// the delimiter of the expansion's here-document leaving it as typed.
// History gets it as one line, and the screen the request, not the line
// it was rewritten to. An even run of backslashes is sent as it is, a
// command's backslash is bash's line continuation still, and a line
// emptied after the backslash is a command again.
func TestBackslashEnter(t *testing.T) {
	const bashrc = `V=world AISH_BIN=fake
fake() { [[ $2 == start ]] && printf '%s\x1f' "$4" >>sent; }
`
	dir, printed := typed(t, "", bashrc, "__aish_route_expand=true\r"+
		"Say $V \\\rnext\r"+
		`Say a \\`+"\r"+
		`Say b \\\`+"\rc\r"+
		"echo a \\\rb >>out\r"+
		"Say $V \\\r\r"+
		"? Raw $V \\\r$(echo hi)\r"+
		"Say $(echo x >>count) \\\rmore\r"+
		"Say $V \\\r__aish_eof \\\r: >joined\r"+
		"Say \\\r\x15echo ok >>out\r"+
		"history >hist\r")
	read := func(name string) string {
		b, _ := os.ReadFile(filepath.Join(dir, name))
		return string(b)
	}
	want := []string{"Say world\nnext", `Say a \\`, `Say b \\` + "\nc", "Say world", "Raw $V\n$(echo hi)",
		"Say \nmore", "Say $V\n__aish_eof\n: >joined", ""}
	if got := strings.Split(read("sent"), "\x1f"); strings.Join(got, "\x1f") != strings.Join(want, "\x1f") {
		t.Errorf("sent:\n%q\nwant\n%q\n%s", got, want, printed)
	}
	if got := read("out"); got != "a b\nok\n" {
		t.Errorf("the commands wrote %q, want %q", got, "a b\nok\n")
	}
	if got := read("count"); got != "x\n" {
		t.Errorf("$(...) ran %q times, want once, after the last line", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "joined")); err == nil {
		t.Error("a line after the delimiter ran")
	}
	for _, h := range []string{"Say $V\nnext\n", "Say $V\n__aish_eof\n: >joined\n"} {
		if !strings.Contains(read("hist"), h) {
			t.Errorf("history has no %q:\n%s", h, read("hist"))
		}
	}

	_, printed = typed(t, "", bashrc, "Say $V \\\rnext\r")
	s := newScreen(80, 24)
	s.write(t, regexp.MustCompile("\x1b]6973;[^\a]*\a").ReplaceAllString(printed, ""))
	screen := strings.Join(append(s.history, s.lines()...), "\n")
	if strings.Contains(screen, "__aish_ask") || !strings.Contains(screen, "\n> ? Say $V\nnext\n> exit") {
		t.Errorf("the screen:\n%s", screen)
	}
}

// TestZshBackslashEnter is TestBackslashEnter for zsh, whose zle keeps the
// line when the widget accept-line returns without accepting it.
func TestZshBackslashEnter(t *testing.T) {
	const zshrc = `X=world AISH_BIN=fake
fake() { [[ $2 == start ]] && print -rn -- "$4"$'\x1f' >>$HOME/sent }
`
	pause := 200 * time.Millisecond
	dir, printed := zshTyped(t, zshrc,
		zstep{prompts: 1, keys: "Say $X \\\r"}, zstep{delay: pause, keys: "next\r"},
		zstep{prompts: 2, keys: `Say a \\` + "\r"},
		zstep{prompts: 3, keys: `Say b \\\` + "\r"}, zstep{delay: pause, keys: "c\r"},
		zstep{prompts: 4, keys: "echo a \\\r"}, zstep{delay: pause, keys: "b >>out\r"},
		zstep{prompts: 5, keys: "Say $X \\\r"}, zstep{delay: pause, keys: "$(echo hi)\r"},
		zstep{prompts: 6, keys: "fc -ln 1 >hist\r"},
		zstep{prompts: 7},
	)
	read := func(name string) string {
		b, _ := os.ReadFile(filepath.Join(dir, name))
		return string(b)
	}
	want := []string{"Say world\nnext", `Say a \\`, `Say b \\` + "\nc", "Say world\nhi", ""}
	if got := strings.Split(read("sent"), "\x1f"); strings.Join(got, "\x1f") != strings.Join(want, "\x1f") {
		t.Errorf("sent:\n%q\nwant\n%q\n%q", got, want, printed)
	}
	if got := read("out"); got != "a b\n" {
		t.Errorf("the command wrote %q, want %q", got, "a b\n")
	}
	// fc -l writes the newline of an entry as \n.
	if !strings.Contains(read("hist"), `Say $X\nnext`+"\n") {
		t.Errorf("history:\n%s", read("hist"))
	}
}
