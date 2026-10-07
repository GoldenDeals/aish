package shellinit

import (
	"strings"
	"testing"
)

// TestExpandSingleQuotes checks what single quotes in a request keep: the
// text in them reaches the model as typed, quotes included, and nothing in
// them runs. An apostrophe in a word opens no quote, a quote without a pair
// is text, and in $(...) and ${...} quotes stay shell syntax. Where the walk
// of the text cannot follow bash for sure (case in $(...), posix mode), the
// rest is expanded as before.
func TestExpandSingleQuotes(t *testing.T) {
	const route = "capital=true\nnot_found=true\nsuffix=?\nmin_words=2\nexpand=true\n"
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
		// The quote would end in $(...), where bash is in a quote: kept
		// as before, the $(...) in it quoted in the substitution.
		{"Is '$(echo ' $(echo X) ')' safe?", "Is ' $(echo X) ' safe?"},
		// Not followed: case in $(...) ends the walk.
		{"Is $(case a in a) echo A;; esac) '$V' kept?", "Is A 'world' kept?"},
		// A line continuation joins $ and ( for bash.
		{"Is $\\\n(echo A) '$V' kept?", "Is A 'world' kept?"},
	}
	var script strings.Builder
	for _, c := range cases {
		script.WriteString("__aish_fresh=1; READLINE_LINE=" + quote(c.in) + "; __aish_route; " + printLine +
			"; w=" + quote(c.want) + "; printf '__aish_ask %s\\x1f' \"${w@Q}\"\n")
	}
	// Posix mode reads a quote in ${...} as text: the walk stops there.
	script.WriteString("set -o posix; __aish_fresh=1; READLINE_LINE=" + quote("Is ${NOPE:-'}'} '$V' here?") +
		"; __aish_route; set +o posix; " + printLine + "; w=" + quote("Is ''} 'world' here?") +
		"; printf '__aish_ask %s\\x1f' \"${w@Q}\"\n")
	// What is quoted does not run; what is not, does.
	for _, in := range []string{"Does '$(: >quoted)' run?", "Does $(: >bare) run?"} {
		script.WriteString("__aish_fresh=1; READLINE_LINE=" + quote(in) + "; __aish_route\n")
	}
	script.WriteString(`printf '%s %s\x1f' "$([[ -e quoted ]] && echo ran || echo kept)" "$([[ -e bare ]] && echo ran || echo kept)"` + "\n")
	got := routed(t, route, "V=world", script.String())
	n := len(cases) + 1
	if len(got) < 2*n+1 {
		t.Fatalf("output %q", got)
	}
	for i := 0; i < n; i++ {
		if got[2*i] != got[2*i+1] {
			t.Errorf("%d: %q, want %q", i, got[2*i], got[2*i+1])
		}
	}
	if g := got[2*n]; g != "kept ran" {
		t.Errorf("$(...) in quotes, then bare: %q, want %q", g, "kept ran")
	}
}
