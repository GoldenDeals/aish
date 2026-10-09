package shellinit

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// expandOn is the route of tests that expand a request: [route] expand is
// off by default, the rest stays init.bash's.
const expandOn = "expand=true\n"

// routed runs script in an interactive bash that loaded bashrc, then
// init.bash with $AISH_RUN/route holding route, in a home of its own so
// that no skill of the machine is found. It returns the output, markers
// stripped, split at \x1f.
func routed(t *testing.T, route, bashrc, script string) []string {
	t.Helper()
	dir := t.TempDir()
	run := filepath.Join(dir, "run")
	init := filepath.Join(dir, "init.bash")
	files := map[string]string{
		init:                        Bash,
		filepath.Join(run, "nonce"): "N\n",
		filepath.Join(run, "route"): route,
	}
	for p, s := range files {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("bash", "--norc", "--noprofile", "-i")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(bashrc + "\nsource " + init + " 2>/dev/null\n" + script)
	cmd.Env = cleanEnv("PS1=", "PS2=", "HISTFILE=/dev/null", "LC_ALL=C.UTF-8",
		"HOME="+dir, "XDG_CONFIG_HOME="+filepath.Join(dir, "xdg"), "AISH_RUN="+run)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(regexp.MustCompile("\x1b]6973;[^\a]*\a").ReplaceAllString(string(out), ""), "\x1f")
}

// TestRouteConfig checks what [route] sends to the assistant: a line that
// is words and no command (the typo of two words included), one ending
// with the suffix, one with a capital letter, each turned off in turn. A
// command, a single word and a line with shell syntax stay bash's.
func TestRouteConfig(t *testing.T) {
	for _, tc := range []struct {
		route string
		cases []struct{ in, want string }
	}{
		{"capital=true\nnot_found=true\nsuffix=?\nmin_words=2\n", []struct{ in, want string }{
			{"why does this fail", "__aish_ask 'why does this fail'"},
			{"  почему не собирается", "__aish_ask 'почему не собирается'"},
			{"why doesn't it work", `__aish_ask 'why doesn'\''t it work'`}, // a quote is no shell syntax
			{"gti status", "__aish_ask 'gti status'"},                      // the price of the rule
			{"gti", "gti"},
			{"gti status | head", "gti status | head"},
			{"x y;z", "x y;z"},
			{"x y&", "x y&"},
			{"x y>z", "x y>z"},
			{"x y<z", "x y<z"},
			{"x (y)", "x (y)"},
			{"x $y", "x $y"},
			{"x `y`", "x `y`"},
			{`x \y`, `x \y`},
			{"what is a=b", "what is a=b"},
			{"echo hello world", "echo hello world"},
			{"myfn a b", "myfn a b"},
			{"ls?", "__aish_ask 'ls?'"}, // `ls?` is no command
			{"ls foo?", "ls foo?"},      // `ls` is, and foo? a glob
			{"what is it?  ", "__aish_ask 'what is it?'"},
			{"Find big files", "__aish_ask 'Find big files'"},
			{"?ls", "__aish_ask 'ls'"},
		}},
		{"capital=false\nnot_found=false\nsuffix=\nmin_words=2\n", []struct{ in, want string }{
			{"why does this fail", "why does this fail"},
			{"Find big files", "Find big files"},
			{"nosuch?", "nosuch?"},
			{"?why", "__aish_ask 'why'"},
		}},
		{"capital=true\nnot_found=true\nsuffix==?\nmin_words=3\n", []struct{ in, want string }{
			{"gti status", "gti status"},
			{"gti status now", "__aish_ask 'gti status now'"},
			{"so =?", "__aish_ask 'so =?'"}, // the value is all after the first =
			{"so?", "so?"},
		}},
		{"capital=true\nnot_found=true\nsuffix=?\nmin_words=1\n", []struct{ in, want string }{
			{"gti", "__aish_ask 'gti'"},
			{"gti|x", "gti|x"},
		}},
	} {
		var script strings.Builder
		for _, c := range tc.cases {
			script.WriteString("__aish_fresh=1; READLINE_LINE=" + quote(c.in) + "; __aish_route; " + printLine + "\n")
		}
		got := routed(t, tc.route, "myfn() { :; }", script.String())
		for i, c := range tc.cases {
			if i >= len(got) {
				t.Fatalf("%q: missing output for %q (got %q)", tc.route, c.in, got)
			}
			if got[i] != c.want {
				t.Errorf("%q: %q -> %q, want %q", tc.route, c.in, got[i], c.want)
			}
		}
	}
}

// TestNotFoundHandle runs lines bash finds no command for: the handler
// from ~/.bashrc answers them either way, and with not_found off the first
// line that looks like a request gets a hint after it, once.
func TestNotFoundHandle(t *testing.T) {
	const (
		on   = "capital=true\nnot_found=true\nsuffix=?\nmin_words=2\n"
		off  = "capital=true\nnot_found=false\nsuffix=?\nmin_words=2\n"
		prev = `command_not_found_handle() { printf 'prev:%s\n' "$*"; return 127; }`
		hint = "aish: looks like a question; prefix with ? to ask\n"
	)
	for _, tc := range []struct {
		route, bashrc string
		cases         []struct{ in, want string }
	}{
		{on, prev, []struct{ in, want string }{
			{"gti", "prev:gti\nrc=127"},
			{"gti status | head", "prev:gti status\nrc=0"},
		}},
		{off, prev, []struct{ in, want string }{
			{"gti", "prev:gti\nrc=127"},
			{"why does this fail", "prev:why does this fail\n" + hint + "rc=127"},
			{"why does this fail", "prev:why does this fail\nrc=127"},
			{"gti status | head", "prev:gti status\nrc=0"},
		}},
		{off, "", []struct{ in, want string }{
			{"gti", "bash: gti: command not found\nrc=127"},
			{"why does this fail", "bash: why: command not found\n" + hint + "rc=127"},
		}},
	} {
		var script strings.Builder
		for _, c := range tc.cases {
			script.WriteString("__aish_fresh=1; READLINE_LINE=" + quote(c.in) + "; __aish_route; { eval \"$READLINE_LINE\"; } 2>&1; printf 'rc=%s\\x1f' \"$?\"\n")
		}
		// Not the hint's handler when nothing needs it.
		script.WriteString("if declare -F __aish_cnf_prev >/dev/null; then printf wrapped; fi\n")
		got := routed(t, tc.route, tc.bashrc, script.String())
		for i, c := range tc.cases {
			if i >= len(got) {
				t.Fatalf("%q: missing output for %q (got %q)", tc.route, c.in, got)
			}
			if got[i] != c.want {
				t.Errorf("%q, %q: %q -> %q, want %q", tc.route, tc.bashrc, c.in, got[i], c.want)
			}
		}
		if wrapped := got[len(got)-1] == "wrapped"; wrapped != (tc.route == off && tc.bashrc != "") {
			t.Errorf("%q, %q: the handler from ~/.bashrc wrapped: %v", tc.route, tc.bashrc, wrapped)
		}
	}
}
