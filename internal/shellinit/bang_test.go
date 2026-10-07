package shellinit

import (
	"strings"
	"testing"
)

// TestBang routes lines that start with `!`, all of them bash's: `!cmd`
// loses the `!`, history expansion (`!!`, `!$`, `!-2`, `!12`) keeps it,
// and so does bash's negation, `!` before a blank or alone, which must
// still negate when the line runs: `! [ -f keep ] && echo DEL` echoes
// nothing while keep is there.
func TestBang(t *testing.T) {
	cases := []struct{ in, line, run string }{
		{"! true", "! true", "rc=1"},
		{"!\ttrue", "!\ttrue", "rc=1"},
		{"  ! true", "  ! true", "rc=1"},
		{"! ", "! ", "rc=1"},
		{"!", "!", "rc=1"},
		{"! [ -f keep ] && echo DEL", "! [ -f keep ] && echo DEL", "rc=1"},
		{"! [ -f gone ] && echo DEL", "! [ -f gone ] && echo DEL", "DEL\nrc=0"},
		{"! Find big files", "! Find big files", ""}, // no request either
		{"!true", "true", "rc=0"},
		{"!false", "false", "rc=1"},
		{"!ls keep", "ls keep", "keep\nrc=0"},
		{"!Find big files", "Find big files", ""},
		{"!!", "!!", ""},
		{"!$", "!$", ""},
		{"!-2", "!-2", ""},
		{"!12", "!12", ""},
	}
	var script strings.Builder
	for _, c := range cases {
		// Readline reads the script: a tab typed would complete.
		in := strings.ReplaceAll(quote(c.in), "\t", `'$'\t''`)
		script.WriteString("__aish_fresh=1; READLINE_LINE=" + in + "; __aish_route; " + printLine + "; " +
			// The line goes to bash: PS0 has its cmd-start.
			`if [[ $__aish_ps0 == $'\e]6973;N;cmd-start;'"$READLINE_LINE"$'\a' ]]; then printf 'bash\x1f'; else printf 'no mark\x1f'; fi`)
		if c.run != "" {
			script.WriteString(`; { eval "$READLINE_LINE"; } 2>&1; printf 'rc=%s\x1f' "$?"`)
		}
		script.WriteString("\n")
	}
	got := routed(t, "", ": >keep", script.String())
	for _, c := range cases {
		want := []string{c.line, "bash"}
		if c.run != "" {
			want = append(want, c.run)
		}
		if len(got) < len(want) {
			t.Fatalf("missing output for %q (got %q)", c.in, got)
		}
		for i, w := range want {
			if got[i] != w {
				t.Errorf("%q: %q, want %q", c.in, got[i], w)
			}
		}
		got = got[len(want):]
	}
}
