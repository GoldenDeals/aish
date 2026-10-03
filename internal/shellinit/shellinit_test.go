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
	// A home and a directory of its own: a skill of the machine named like
	// a case (gti) would send the line to the model.
	cmd := exec.Command("bash", "--norc", "--noprofile", "-i")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(script.String())
	cmd.Env = cleanEnv("PS1=", "HISTFILE=/dev/null", "LC_ALL=C.UTF-8",
		"HOME="+dir, "XDG_CONFIG_HOME="+filepath.Join(dir, "xdg"))
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

// TestMarkerNonce checks that every marker init.bash prints carries the
// nonce from $AISH_RUN/nonce, the only ones the proxy takes, and no BEL or
// ESC in its payload, even from the name of the current directory.
func TestMarkerNonce(t *testing.T) {
	run := t.TempDir()
	wd := filepath.Join(run, "a\ab\x1bc")
	if err := os.Mkdir(wd, 0o755); err != nil {
		t.Fatal(err)
	}
	init := filepath.Join(run, "init.bash")
	stub := filepath.Join(run, "aish")
	files := map[string]string{
		init:                          Bash,
		filepath.Join(run, "nonce"):   "N0NCE\n",
		filepath.Join(run, "next.id"): "",
		// `agent start` hands one command to the shell, `agent resume` ends.
		stub: "#!/bin/sh\n[ \"$2\" = start ] && echo id1 >\"$AISH_RUN/next.id\" && printf true >\"$AISH_RUN/next.cmd\"\nexit 0\n",
	}
	for p, s := range files {
		if err := os.WriteFile(p, []byte(s), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// No PROMPT_COMMAND: each prompt would add a cmd-end.
	script := "source " + init + "; PROMPT_COMMAND=\n" +
		"__aish_buf=ls; __aish_mark; printf %s \"$__aish_ps0\"\n" +
		"__aish_precmd\n" +
		"__aish_ask 'Hi'\n"
	cmd := exec.Command("bash", "--norc", "--noprofile", "-i")
	cmd.Dir = wd
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = cleanEnv("PS1=", "HISTFILE=/dev/null", "AISH_RUN="+run, "AISH_BIN="+stub,
		"HOME="+run, "XDG_CONFIG_HOME="+filepath.Join(run, "xdg"))
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, m := range regexp.MustCompile("\x1b]6973;([^\a]*)\a").FindAllStringSubmatch(string(out), -1) {
		nonce, rest, _ := strings.Cut(m[1], ";")
		if nonce != "N0NCE" {
			t.Errorf("marker %q without the nonce", m[0])
		}
		if strings.Contains(rest, "\x1b") {
			t.Errorf("marker %q holds an ESC", m[0])
		}
		kind, payload, _ := strings.Cut(rest, ";")
		if kind == "cmd-end" && payload != "0;"+filepath.Join(run, "abc") {
			t.Errorf("cmd-end payload %q", payload)
		}
		kinds = append(kinds, kind)
	}
	if want := "cmd-start cmd-end ask-start agent-start agent-end"; strings.Join(kinds, " ") != want {
		t.Errorf("markers %v, want %s", kinds, want)
	}
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
