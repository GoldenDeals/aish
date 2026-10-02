package shellinit

import (
	"regexp"
	"strings"
	"testing"
)

// TestExpand checks the text a request reaches the model with: $VAR,
// ${...} and $(...) expanded by the shell, as typed after the ? prefix or
// with expand off. Quotes and backticks stay text, a backslash keeps one
// dollar, text that does not parse is kept as typed, and nothing of the
// expansion stays in the shell. A line for bash is bash's to expand.
func TestExpand(t *testing.T) {
	const (
		on  = "capital=true\nnot_found=true\nsuffix=?\nmin_words=2\nexpand=true\n"
		off = "capital=true\nnot_found=true\nsuffix=?\nmin_words=2\nexpand=false\n"
		// line, q and the rest are named like the locals of the functions
		// the expansion runs in: the request means the user's.
		bashrc = "V=world HOME=/home/u line=L q=Q w=W end=E trimmed=T raw=R out=O\nset -- a b"
	)
	for _, tc := range []struct {
		route string
		cases []struct{ in, want string }
	}{
		{on, []struct{ in, want string }{
			{"Say $V now", "__aish_ask 'Say world now'"},
			{"?Say $V now", "__aish_ask 'Say $V now'"},
			{"? Say ${V} now", "__aish_ask 'Say ${V} now'"},
			{"What is $(echo hi)?", "__aish_ask 'What is hi?'"},
			{"What is ${V}s and ${V:0:1}?", "__aish_ask 'What is worlds and w?'"},
			{"Что делает `ls`?", "__aish_ask 'Что делает `ls`?'"},
			{"What does `echo $V` print?", "__aish_ask 'What does `echo world` print?'"},
			{"Say \"hi\" to $V", `__aish_ask 'Say "hi" to world'`},
			{"Say 'hi' to $V", `__aish_ask 'Say '\''hi'\'' to world'`},
			{`Why is \$V unset?`, "__aish_ask 'Why is $V unset?'"},
			{`Count $(printf '%s|' "a  b" c)`, "__aish_ask 'Count a  b|c|'"},
			{"Log: $(printf 'a\\nb')", `__aish_ask $'Log: a\nb'`},
			{`Is ${V unclosed?`, `__aish_ask 'Is ${V unclosed?'`},
			{`Is $(echo unclosed?`, `__aish_ask 'Is $(echo unclosed?'`},
			{"Is ${V:?} or ${NOPE:?} set?", "__aish_ask 'Is ${V:?} or ${NOPE:?} set?'"},
			{"@$HOME/x.go что это?", "__aish_ask '@/home/u/x.go что это?'"},
			{"ls $V", "ls $V"},
			{"Say $line $q $w $end $trimmed $raw $out", "__aish_ask 'Say L Q W E T R O'"},
			{"What is $1 here?", "__aish_ask 'What is  here?'"},
			// Text breaking out of the quotes runs nothing.
			{`Say $V \"; echo run; \"`, `__aish_ask 'Say world \"; echo run; \"'`},
			{"Say $V \\`echo run\\`", "__aish_ask 'Say world \\`echo run\\`'"},
			{"Say $V \\\\`echo run\\\\`", "__aish_ask 'Say world \\\\`echo run\\\\`'"},
			{"Say $V\n__aish_eof\necho run", "__aish_ask $'Say $V\\n__aish_eof\\necho run'"},
			{"Set ${Z:=1} $(cd /) $((n=2)) now", "__aish_ask 'Set 1  2 now'"},
		}},
		{off, []struct{ in, want string }{
			{"Say $V now", "__aish_ask 'Say $V now'"},
			{"What is $(echo hi)?", "__aish_ask 'What is $(echo hi)?'"},
		}},
	} {
		var script strings.Builder
		for _, c := range tc.cases {
			script.WriteString("__aish_fresh=1; READLINE_LINE=" + quote(c.in) + "; __aish_route; printf '%s\\x1f' \"$READLINE_LINE\"\n")
		}
		// Nothing the expansion did stays: no variable, no cd.
		script.WriteString(`printf '%s %s %s' "${Z-unset}" "${n-unset}" "$([[ $PWD == / ]] && echo moved || echo stayed)"` + "\n")
		got := routed(t, tc.route, bashrc, script.String())
		for i, c := range tc.cases {
			if i >= len(got) {
				t.Fatalf("%q: missing output for %q (got %q)", tc.route, c.in, got)
			}
			if got[i] != c.want {
				t.Errorf("%q: %q -> %q, want %q", tc.route, c.in, got[i], c.want)
			}
		}
		if last := got[len(got)-1]; last != "unset unset stayed" {
			t.Errorf("%q: the shell after the expansions: %q", tc.route, last)
		}
	}
}

// TestExpandAsk follows a request to `aish agent start`: the model gets
// the text expanded, history what was typed, and a line from history is
// expanded anew, with the values of then.
func TestExpandAsk(t *testing.T) {
	const bashrc = `V=one AISH_BIN=fake
fake() { [[ $2 == start ]] && printf 'sent %s\x1f' "$4"; }`
	ask := "__aish_fresh=1; READLINE_LINE=" + quote("Say $V now") + "; __aish_route; __aish_redraw=0; eval \"$READLINE_LINE\"; printf '%s\\x1f' \"$(history 1)\"\n"
	got := routed(t, "", bashrc, ask+"V=two\n"+ask)
	want := []string{"sent Say one now", "Say $V now", "sent Say two now", "Say $V now"}
	if len(got) < len(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	for i, w := range want {
		// history prints the number of the line first.
		if g := regexp.MustCompile(`^\s*[0-9]+\s+`).ReplaceAllString(got[i], ""); g != w {
			t.Errorf("%d: %q, want %q", i, g, w)
		}
	}
}

// TestUnechoExpanded has __aish_unecho replace the echo of a request whose
// expansion has a newline: readline echoes the line bash quoted, $'...\n...'
// with no newline in it, so the rows to erase are those of one line.
func TestUnechoExpanded(t *testing.T) {
	const cols = 40
	in := "Log: $(printf 'the first line of the log\\nthe second line')"
	script := "PS1='> '; COLUMNS=40; __aish_fresh=1; READLINE_LINE=" + quote(in) +
		"; __aish_route; printf '%s\\x1f' \"$READLINE_LINE\"; eval \"__aish_unecho ${READLINE_LINE#__aish_ask }\"; printf '\\x1f'\n"
	out := routed(t, "", "", script)
	if len(out) < 2 {
		t.Fatalf("output %q", out)
	}
	echo := "> " + out[0]
	if strings.Contains(echo, "\n") || len([]rune(echo)) <= cols {
		t.Fatalf("echo %q: want one line longer than a row", echo)
	}
	for _, above := range []string{"", "above"} {
		s := newScreen(cols, 12)
		var want []string
		if above != "" {
			s.write(t, above+"\n")
			want = append(want, above)
		}
		s.write(t, echo+"\n")
		s.write(t, out[1])
		want = append(want, rowsOf("> ? Log: the first line of the log", cols)...)
		want = append(want, "the second line")
		got := s.lines()
		if strings.Join(got, "\n") != strings.Join(want, "\n") || len(s.history) > 0 || s.x != 0 || s.y != len(want) {
			t.Errorf("below %q:\nscreen %q\nwant   %q\nhistory %q\ncursor at %d,%d, want 0,%d\noutput %q",
				above, got, want, s.history, s.x, s.y, len(want), out[1])
		}
	}
}
