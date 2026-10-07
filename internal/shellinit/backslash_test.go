package shellinit

import (
	"strings"
	"testing"
)

// TestExpandBackslashes checks the backslashes of an expanded request: they
// reach the model as typed, a backslash-newline and \\ too, but for \$, a
// dollar, as before. A run of them before a substitution keeps its parity:
// \$(...) does not run, \\$(...) does. In $(...) and ${...} they are shell
// syntax, as before. A line that is the here-document's delimiter once
// joined at a backslash-newline would end it early and run the lines after
// it: the text stays as typed, and nothing in it runs.
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
		{`Say $V \`, `Say world \`},
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
