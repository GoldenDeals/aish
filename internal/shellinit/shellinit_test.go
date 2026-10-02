package shellinit

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestSyntax(t *testing.T) {
	cmd := exec.Command("bash", "-n")
	cmd.Stdin = strings.NewReader(Bash)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// TestRoute feeds lines to __aish_route in an interactive bash and checks
// what the line is rewritten to.
func TestRoute(t *testing.T) {
	dir := t.TempDir()
	init := filepath.Join(dir, "init.bash")
	if err := os.WriteFile(init, []byte(Bash), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ in, want string }{
		{"ls -la", "ls -la"},
		{"ll", "ll"}, // alias
		{"myfn arg", "myfn arg"},
		{"FOO=1 make", "FOO=1 make"},
		{"./run.sh", "./run.sh"},
		{"cd ..; ls", "cd ..; ls"},
		{"if true; then echo; fi", "if true; then echo; fi"},
		{"Как найти большие файлы?", "__aish_ask 'Как найти большие файлы?'"},
		{"What's in this dir", `__aish_ask 'What'\''s in this dir'`},
		{"Find big files", "__aish_ask 'Find big files'"}, // `find` is a command, `Find` is not
		{"как найти большие файлы", "как найти большие файлы"},
		{"gti status", "gti status"}, // a typo stays with bash
		{"PATH=/x ls", "PATH=/x ls"},
		{"?ls", "__aish_ask 'ls'"},
		{"? explain this", "__aish_ask 'explain this'"},
		{"@main.go what is it", "__aish_ask '@main.go what is it'"},
		{"!hello there", "hello there"},
		{"!!", "!!"},
		{"", ""},
	}
	var script strings.Builder
	script.WriteString("alias ll='ls -l'\nmyfn() { :; }\nsource " + init + " 2>/dev/null\n")
	for _, c := range cases {
		script.WriteString("__aish_fresh=1; READLINE_LINE=" + quote(c.in) + "; __aish_route; printf '%s\\x1f' \"$READLINE_LINE\"\n")
	}
	cmd := exec.Command("bash", "--norc", "--noprofile", "-i")
	cmd.Stdin = strings.NewReader(script.String())
	cmd.Env = append(os.Environ(), "PS1=", "HISTFILE=/dev/null", "LC_ALL=C.UTF-8")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(regexp.MustCompile("\x1b]6973;[^\a]*\a").ReplaceAllString(string(out), ""), "\x1f")
	for i, c := range cases {
		if i >= len(got) {
			t.Fatalf("missing output for %q (got %q)", c.in, out)
		}
		if got[i] != c.want {
			t.Errorf("%q -> %q, want %q", c.in, got[i], c.want)
		}
	}
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
