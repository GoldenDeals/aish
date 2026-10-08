package shellinit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// zshSent routes each of ins as Enter does in a zsh with V=world after rc
// and returns the text each request goes with. The shell runs in the
// directory returned.
func zshSent(t *testing.T, rc string, ins []string) ([]string, string) {
	t.Helper()
	var script strings.Builder
	script.WriteString(zshLine + zshExpandLine + "__aish_sent() { __aish_testx 0 \"$1\" >/dev/null; print -rn -- \"$__aish_req\"$'\\x1f'; }\n")
	for _, in := range ins {
		script.WriteString("__aish_sent " + quote(in) + "\n")
	}
	got, run := zshRun(t, "", "V=world\n"+rc, script.String())
	return got, filepath.Dir(run)
}

// TestZshExpandAliases: an alias is code zsh reads in $(...), a global one
// anywhere. With one that leaves a parenthesis or a quote open the walk
// cannot know where $(...) ends: a text with a quote that starts a word
// stays as typed, and what the alias would put out of the quote does not
// run. Aliases that close what they open change nothing.
func TestZshExpandAliases(t *testing.T) {
	for _, tc := range []struct{ rc, in, want string }{
		{"alias -g PP=')'", "Is $(echo PP '$(: >galias)' ) here?", "Is $(echo PP '$(: >galias)' ) here?"},
		{"alias -g QQ=\"'\"", "Is $(echo QQ) '$(: >quote)' QQ) here?", "Is $(echo QQ) '$(: >quote)' QQ) here?"},
		{"alias cc=case", "Is $(cc a in a) echo A;; esac) '$V' here?", "Is $(cc a in a) echo A;; esac) '$V' here?"},
		{"alias ll='ls -l' q='echo \"x\"'\nalias -g G='| head'", "Is $(echo hi G) '$V' here?", "Is hi '$V' here?"},
		{"alias -g PP=')'", "Is $(echo PP) here?", "Is $(echo PP) here?"}, // as before: zsh fails to read it
	} {
		got, dir := zshSent(t, tc.rc, []string{tc.in})
		if at(got, 0) != tc.want {
			t.Errorf("%s: %q:\n got %q\nwant %q", tc.rc, tc.in, at(got, 0), tc.want)
		}
		for _, f := range []string{"galias", "quote"} {
			if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
				t.Errorf("%s: %s ran", tc.rc, f)
			}
		}
	}
}

// TestZshExpandQuotes has the cases of TestExpandSingleQuotes give the
// same text in zsh: single quotes of the top level keep what is in them, an
// apostrophe in a word opens none, and in $(...) quotes are shell syntax.
// Where the walk stops (case in $(...), a line continuation after $), zsh
// keeps a text with a quote that starts a word as typed, as it did before,
// rather than expand what is in its quotes as bash does. Nothing quoted
// runs.
func TestZshExpandQuotes(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Why is '$V' empty and $V set?", "Why is '$V' empty and world set?"},
		{"What does '$(echo hi)' print?", "What does '$(echo hi)' print?"},
		{"Why doesn't $V work, isn't it set?", "Why doesn't world work, isn't it set?"},
		{"Is 'it's $V' right?", "Is 'it's $V' right?"},
		{"Is '$V's value set?", "Is 'world's value set?"},
		{"Rock 'n' roll $V", "Rock 'n' roll world"},
		{`Count $(printf '%s|' "a  b" c) and '$V'`, "Count a  b|c| and '$V'"},
		{"Is ${NOPE:-'x y'} or '$V'?", "Is 'x y' or '$V'?"},
		{`Say "'$V'" and "$V"`, `Say "'$V'" and "world"`},
		{"What does `echo '$V'` print, and `echo $V`?", "What does `echo '$V'` print, and `echo world`?"},
		{"Are '\\$V' and '\\`x\\`' kept?", "Are '\\$V' and '\\`x\\`' kept?"},
		{`Is it $'\n' or '$V'?`, `Is it $'\n' or '$V'?`},
		{`Say \'$V\'`, `Say \'world\'`},
		{"Is '$V\nstill $V' quoted?", "Is '$V\nstill $V' quoted?"},
		{"Is 'a\\\nb' $V", "Is 'a\\\nb' world"},
		{"Say '$V'\n__aish_eof\necho run", "Say '$V'\n__aish_eof\necho run"},
		{"Почему '$V' и '$(echo hi)' не раскрылись, а $V раскрылся?", "Почему '$V' и '$(echo hi)' не раскрылись, а world раскрылся?"},
		{"Is слово'$V' or «'$V'» quoted?", "Is слово'world' or «'$V'» quoted?"},
		{"Is '$(echo ' $(echo X) ')' safe?", "Is ' $(echo X) ' safe?"},
		// Not followed: as typed, where bash gives "Is A 'world' kept?".
		{"Is $(case a in a) echo A;; esac) '$V' kept?", "Is $(case a in a) echo A;; esac) '$V' kept?"},
		{"Is $\\\n(echo A) '$V' kept?", "Is $\\\n(echo A) '$V' kept?"},
		// zsh's own: what bash and zsh read otherwise stops the walk.
		{"Is ${NOPE:-'}'} '$(: >brace)' x?", "Is ${NOPE:-'}'} '$(: >brace)' x?"},
		{"Is ${NOPE:-\\\"} '$(: >dquote)' \"} x?", "Is ${NOPE:-\\\"} '$(: >dquote)' \"} x?"},
		{"Is $(( 1 ' )) $(: >math) ' and '$V'?", "Is $(( 1 ' )) $(: >math) ' and '$V'?"},
		{"Is $(echo; (( 1 ' )); : >cmath ') and '$V'?", "Is $(echo; (( 1 ' )); : >cmath ') and '$V'?"},
		{"Is $(echo ${NOPE:-'a b'}) '$V'?", "Is a b '$V'?"},
		{"Is \"${NOPE:-'$V'}\" and '$V'?", "Is \"'world'\" and '$V'?"},
		{"Say 'unclosed $V", "Say 'unclosed world"},
	}
	ins := make([]string, len(cases))
	for i, c := range cases {
		ins[i] = c.in
	}
	ins = append(ins, "Does '$(: >quoted)' run?", "Does $(: >bare) run?")
	got, dir := zshSent(t, "", ins)
	for i, c := range cases {
		if at(got, i) != c.want {
			t.Errorf("%q:\n got %q\nwant %q", c.in, at(got, i), c.want)
		}
	}
	for _, f := range []string{"quoted", "brace", "dquote", "math", "cmath"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			t.Errorf("%s ran", f)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "bare")); err != nil {
		t.Errorf("$(: >bare) did not run: %v", err)
	}
}

// TestZshExpandBackslashes has the cases of TestExpandBackslashes give the
// same text in zsh: backslashes reach the model as typed, \$ is a dollar,
// a run of them before a substitution keeps its parity, and in $(...) and
// ${...} they are shell syntax. zsh has no here-document to end early: a
// line that would be its delimiter is text.
func TestZshExpandBackslashes(t *testing.T) {
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
		// bash keeps this one as typed: there the line would end its
		// here-document.
		{"Say $V\n__aish_e\\\nof\n: >joined\nok", "Say world\n__aish_e\\\nof\n: >joined\nok"},
		{"Say $[1] $V\n__aish_e\\\nof\n: >joined2\nok", "Say $[1] $V\n__aish_e\\\nof\n: >joined2\nok"},
	}
	ins := make([]string, len(cases))
	for i, c := range cases {
		ins[i] = c.in
	}
	ins = append(ins, `Does \$(: >escaped) run, $V?`, `Does \\$(: >bare) run, $V?`)
	got, dir := zshSent(t, "", ins)
	for i, c := range cases {
		if at(got, i) != c.want {
			t.Errorf("%q:\n got %q\nwant %q", c.in, at(got, i), c.want)
		}
	}
	for _, f := range []string{"escaped", "joined", "joined2"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			t.Errorf("%s ran", f)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "bare")); err != nil {
		t.Errorf(`\\$(: >bare) did not run: %v`, err)
	}
}

// TestZshExpandErrReturn: $? in a request and %? in the prompt redrawn for
// it are the user's last code under his err_return too, which would return
// from the function that sets it.
func TestZshExpandErrReturn(t *testing.T) {
	script := zshLine + zshExpandLine + "setopt errreturn\n__aish_testx 3 'Code was $?'\n" +
		"PS1='%?> '; __aish_rc=3; COLUMNS=80 __aish_unecho text; print -n $'\\x1f'\n"
	got, _ := zshRun(t, "", "", script)
	if want := `__aish_ask Code\ was\ 3`; at(got, 0) != want {
		t.Errorf("request %q, want %q", at(got, 0), want)
	}
	if !strings.HasSuffix(at(got, 1), "3? text\n") {
		t.Errorf("redrawn %q, want the prompt with 3 and the request", at(got, 1))
	}
}
