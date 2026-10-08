package shellinit

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// zshPath is the zsh to test init.zsh with; the test is skipped without
// one.
func zshPath(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("no zsh in PATH")
	}
	return p
}

// zshRun runs script in an interactive zsh without rc files that sourced
// rc and then init.zsh, with $AISH_RUN of its own holding route and the
// nonce N0NCE, in a home of its own. It returns the output, markers
// stripped, split at \x1f, and the run directory.
func zshRun(t *testing.T, route, rc, script string, env ...string) ([]string, string) {
	t.Helper()
	zsh := zshPath(t)
	dir := t.TempDir()
	run := filepath.Join(dir, "run")
	init := filepath.Join(dir, "init.zsh")
	files := map[string]string{
		init:                          Zsh,
		filepath.Join(run, "nonce"):   "N0NCE\n",
		filepath.Join(run, "route"):   route,
		filepath.Join(run, "next.id"): "",
	}
	for p, s := range files {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(zsh, "-f", "-i", "+o", "promptsp")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(rc + "\nsource " + init + "\n" + script)
	cmd.Env = cleanEnv(append([]string{"PS1=", "PS2=", "RPROMPT=", "HISTFILE=/dev/null", "LC_ALL=C.UTF-8",
		"HOME=" + dir, "ZDOTDIR=" + dir, "XDG_CONFIG_HOME=" + filepath.Join(dir, "xdg"), "AISH_RUN=" + run}, env...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return strings.Split(regexp.MustCompile("\x1b]6973;[^\a]*\a").ReplaceAllString(string(out), ""), "\x1f"), run
}

// zshLine is what TestZshRoute prints of a line routed as Enter routes it.
// The helper's own assignments do not warn under the user's options.
const zshLine = `__aish_test() { \setopt localoptions nowarncreateglobal nowarnnestedvar; BUFFER=$1; PREBUFFER=; __aish_route; if [[ -n $__aish_t || -n $__aish_raw ]]; then __aish_request; fi; __aish_t= __aish_raw=; if [[ $BUFFER == '__aish_ask "$__aish_req"' ]]; then \print -rn -- "__aish_ask ${(q)__aish_req}"$'\x1f'; else \print -rn -- "$BUFFER"$'\x1f'; fi; }
`

func TestZshSyntax(t *testing.T) {
	zsh := zshPath(t)
	for name, src := range map[string]string{"init.zsh": Zsh, ".zshenv": ZshEnv, ".zshrc": ZshRC()} {
		cmd := exec.Command(zsh, "-n")
		cmd.Stdin = strings.NewReader(src)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s: %v\n%s", name, err, out)
		}
	}
}

// TestZshRoute feeds lines to __aish_route as Enter does and checks what
// the line becomes: a command stays zsh's, a request is rewritten.
func TestZshRoute(t *testing.T) {
	cases := []struct{ in, want string }{
		{"ls -la", "ls -la"},
		{"ll", "ll"}, // alias
		{"myfn arg", "myfn arg"},
		{"FOO=1 make", "FOO=1 make"},
		{"./run.sh", "./run.sh"},
		{"cd ..; ls", "cd ..; ls"},
		{"if true; then echo; fi", "if true; then echo; fi"},
		{"Как найти большие файлы?", `__aish_ask Как\ найти\ большие\ файлы\?`},
		{"What's in this dir", `__aish_ask What\'s\ in\ this\ dir`},
		{"Find big files", `__aish_ask Find\ big\ files`},
		{"как найти большие файлы", "как найти большие файлы"},
		{"gti status", "gti status"},
		{"?ls", "__aish_ask ls"},
		{"? explain this", `__aish_ask explain\ this`},
		{"?", ""},
		{"@main.go what is it", `__aish_ask @main.go\ what\ is\ it`},
		{"!hello there", "hello there"},
		{"!!", "!!"},
		{"! false", "! false"},
		{"=ls", "=ls"},
		{"  Spaces around  ", `__aish_ask Spaces\ around`},
		{"", ""},
	}
	var script strings.Builder
	script.WriteString(zshLine)
	for _, c := range cases {
		script.WriteString("__aish_test " + quote(c.in) + "\n")
	}
	got, _ := zshRun(t, "", "alias ll='ls -l'\nmyfn() { :; }", script.String())
	for i, c := range cases {
		if i >= len(got) {
			t.Fatalf("missing output for %q (got %q)", c.in, got)
		}
		if got[i] != c.want {
			t.Errorf("%q -> %q, want %q", c.in, got[i], c.want)
		}
	}
}

// TestZshRouteConfig: [route] in $AISH_RUN/route as init.bash reads it.
func TestZshRouteConfig(t *testing.T) {
	cases := []struct{ in, want string }{
		{"why does this fail", `__aish_ask why\ does\ this\ fail`},
		{"gti", "gti"},
		{"gti status | head", "gti status | head"},
		{"what is it?", `__aish_ask what\ is\ it\?`},
		{"Find", "Find"},
	}
	var script strings.Builder
	script.WriteString(zshLine)
	for _, c := range cases {
		script.WriteString("__aish_test " + quote(c.in) + "\n")
	}
	got, _ := zshRun(t, "capital=false\nnot_found=true\nsuffix=?\nmin_words=2\n", "", script.String())
	for i, c := range cases {
		if i >= len(got) || got[i] != c.want {
			t.Errorf("%q -> %q, want %q", c.in, at(got, i), c.want)
		}
	}
}

func at(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return "<none>"
}

// TestZshAsk: a request goes through `aish agent start`, and the command
// the agent leaves runs in the shell itself, between agent-start and
// agent-end with the nonce: its cd and export stay. History gets what was
// typed, not the line Enter rewrote.
func TestZshAsk(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "aish")
	sub := filepath.Join(dir, "a\ab\x1bc")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// `agent start` hands one command to the shell, `agent resume` ends.
	script := "#!/bin/sh\n[ \"$2\" = start ] && echo id1 >\"$AISH_RUN/next.id\" && printf '%s' \"cd '" + sub + "' && export FOO=bar\" >\"$AISH_RUN/next.cmd\"\nexit 0\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	zsh := zshPath(t)
	run := filepath.Join(dir, "run")
	os.MkdirAll(run, 0o755)
	os.WriteFile(filepath.Join(run, "nonce"), []byte("N0NCE\n"), 0o600)
	os.WriteFile(filepath.Join(run, "next.cmd"), nil, 0o600)
	init := filepath.Join(dir, "init.zsh")
	os.WriteFile(init, []byte(Zsh), 0o644)
	// No precmd of its own: each prompt would add a cmd-end.
	in := "source " + init + "; precmd_functions=()\n" + zshLine +
		"__aish_test 'What is this?' >/dev/null\n" +
		"__aish_buf=ls; __aish_mark; __aish_preexec\n" +
		"__aish_precmd\n" +
		"__aish_ask \"$__aish_req\"\n" +
		"print -r -- \"pwd=${PWD//[$'\\a\\e']/} foo=$FOO\"\n" +
		"print -r -- \"history: $(fc -ln 1 | grep -c '^What is this?$') $(fc -ln 1 | grep -c '^__aish_ask ')\"\n"
	cmd := exec.Command(zsh, "-f", "-i", "+o", "promptsp")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(in)
	cmd.Env = cleanEnv("PS1=", "HISTFILE=/dev/null", "LC_ALL=C.UTF-8", "HOME="+dir, "ZDOTDIR="+dir,
		"XDG_CONFIG_HOME="+filepath.Join(dir, "xdg"), "AISH_RUN="+run, "AISH_BIN="+stub)
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	out := string(b)
	var kinds []string
	for _, m := range regexp.MustCompile("\x1b]6973;([^\a]*)\a").FindAllStringSubmatch(out, -1) {
		nonce, rest, _ := strings.Cut(m[1], ";")
		if nonce != "N0NCE" {
			t.Errorf("marker %q without the nonce", m[0])
		}
		if strings.Contains(rest, "\x1b") {
			t.Errorf("marker %q holds an ESC", m[0])
		}
		kind, payload, _ := strings.Cut(rest, ";")
		if kind == "agent-end" && payload != "id1;0;"+filepath.Join(dir, "abc") {
			t.Errorf("agent-end payload %q", payload)
		}
		kinds = append(kinds, kind)
	}
	if want := "cmd-start cmd-end ask-start agent-start agent-end"; strings.Join(kinds, " ") != want {
		t.Errorf("markers %v, want %s", kinds, want)
	}
	if !strings.Contains(out, "pwd="+filepath.Join(dir, "abc")+" foo=bar") {
		t.Errorf("the agent's cd and export did not stay:\n%q", out)
	}
	if !strings.Contains(out, "history: 1 0\n") {
		t.Errorf("history: the request as typed, once, and no line rewritten:\n%q", out)
	}
}

// zshExpandLine routes a line as Enter does, expansion and all, after the
// user's last command left the code $1: __aish_testx CODE LINE. zle -R has
// no zle to show its message in.
const zshExpandLine = "__aish_testx() { __aish_test_rc=$1; shift; BUFFER=$1; PREBUFFER=; __aish_route; __aish_rc=$__aish_test_rc; if [[ -n $__aish_t && -z $__aish_raw && $__aish_t == *'$'* ]]; then __aish_expanding; fi; if [[ -n $__aish_t || -n $__aish_raw ]]; then __aish_request; fi; __aish_t= __aish_raw=; print -rn -- \"__aish_ask ${(q)__aish_req}\"$'\\x1f'; }\nzle() { :; }\n"

// TestZshAskErrReturn: a command of the agent's that fails under the
// user's err_return still ends with agent-end and `aish agent resume`.
func TestZshAskErrReturn(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "aish")
	script := "#!/bin/sh\necho \"$*\" >>\"$AISH_RUN/called\"\n[ \"$2\" = start ] && echo id1 >\"$AISH_RUN/next.id\" && printf false >\"$AISH_RUN/next.cmd\"\nexit 0\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	_, run := zshRun(t, "", "setopt errreturn", "precmd_functions=()\n__aish_ask 'What fails'\n", "AISH_BIN="+stub)
	b, _ := os.ReadFile(filepath.Join(run, "called"))
	if want := "agent start -- What fails\nagent resume id1 1\n"; string(b) != want {
		t.Errorf("called %q, want %q", b, want)
	}
}

// TestZshExpand: a request is expanded as in double quotes, $? being the
// code of the user's last command; single quotes keep what is in them, \$
// is a dollar, a backtick is text, and ? keeps it all.
func TestZshExpand(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Look at $X please", `__aish_ask Look\ at\ world\ please`},
		{"Is $(echo sub) here", `__aish_ask Is\ sub\ here`},
		{"Why doesn't $X work", `__aish_ask Why\ doesn\'t\ world\ work`},
		{"Say 'literal $X' now", `__aish_ask Say\ \'literal\ \$X\'\ now`},
		{`Why \$X here`, `__aish_ask Why\ \$X\ here`},
		{"Run `ls` in $X", "__aish_ask Run\\ \\`ls\\`\\ in\\ world"},
		{"Code was $?", `__aish_ask Code\ was\ 3`},
		{"?Raw $X", `__aish_ask Raw\ \$X`},
		{"Bad ${X", `__aish_ask Bad\ \$\{X`},
		{"Empty $NONE", `__aish_ask Empty`},
	}
	var script strings.Builder
	script.WriteString(zshLine + zshExpandLine + "X=world\n")
	for _, c := range cases {
		script.WriteString("__aish_testx 3 " + quote(c.in) + "\n")
	}
	got, _ := zshRun(t, "", "", script.String())
	for i, c := range cases {
		if at(got, i) != c.want {
			t.Errorf("%q -> %q, want %q", c.in, at(got, i), c.want)
		}
	}
}

// TestZshStartup: zsh started as aish starts it, ZDOTDIR its own, reads
// the user's .zshenv and .zshrc from where they would have been, then
// init.zsh; ZDOTDIR is the user's again, and none of aish's is left in the
// environment.
func TestZshStartup(t *testing.T) {
	zsh := zshPath(t)
	for _, userDot := range []bool{false, true} {
		dir := t.TempDir()
		run := filepath.Join(dir, "run")
		home := filepath.Join(dir, "home")
		user := home
		if userDot {
			user = filepath.Join(dir, "zdot")
		}
		files := map[string]string{
			filepath.Join(run, "zsh", ".zshenv"): ZshEnv,
			filepath.Join(run, "zsh", ".zshrc"):  ZshRC(),
			filepath.Join(run, "nonce"):          "N0NCE\n",
			filepath.Join(user, ".zshenv"):       "ENV_READ=x$ZDOTDIR\n",
			filepath.Join(user, ".zshrc"):        "RC_READ=x$ZDOTDIR\nalias ll='ls -l'\n",
		}
		for p, s := range files {
			os.MkdirAll(filepath.Dir(p), 0o755)
			if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		env := []string{"PS1=", "HISTFILE=/dev/null", "HOME=" + home, "ZDOTDIR=" + filepath.Join(run, "zsh"), "AISH_RUN=" + run}
		if userDot {
			env = append(env, "AISH_ZDOTDIR="+user)
		}
		cmd := exec.Command(zsh, "-i", "+o", "promptsp")
		cmd.Stdin = strings.NewReader(`print -r -- "env=$ENV_READ rc=$RC_READ zdot=${ZDOTDIR-unset} loaded=$__aish_loaded widget=${widgets[accept-line]} aliases=$options[aliases]"; env | grep -c '^AISH_ZDOTDIR=\|^ZDOTDIR=.*/run/zsh$\|__aish'; true` + "\n")
		cmd.Env = cleanEnv(env...)
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v\n%s", err, b)
		}
		out := regexp.MustCompile("\x1b]6973;[^\a]*\a").ReplaceAllString(string(b), "")
		zdot := "unset"
		if userDot {
			zdot = user
		}
		want := "env=x" + strings.TrimSuffix(zdot, "unset") + " rc=x" + strings.TrimSuffix(zdot, "unset") + " zdot=" + zdot + " loaded=1 widget=user:__aish_accept aliases=on\n0\n"
		if out != want {
			t.Errorf("ZDOTDIR of the user %v:\n%q\nwant\n%q", userDot, out, want)
		}
	}
}

// TestZshUserOptions: init.zsh loads and works in a zsh whose options
// would read its code otherwise or warn of what it does, and leaves them
// and the user's aliases as they were; it adds no name of its own but
// __aish_ ones and the hook arrays it adds itself to.
func TestZshUserOptions(t *testing.T) {
	opts := "ksharrays shwordsplit nounset warncreateglobal warnnestedvar rcquotes extendedglob globsubst ignorebraces kshglob shglob cshjunkiequotes printexitvalue errreturn"
	rc := "setopt " + opts + "\nalias -g G='| head' L='| less'\nalias local='echo LOCAL' print='print -P' typeset='typeset -x' setopt='echo SETOPT' unset='echo UNSET'\n" +
		"before=\" ${(j: :)${(k)parameters[@]}} \"\n"
	// The script's own code is read with braces: its functions need them.
	script := "\\unsetopt ignorebraces\n" + zshLine +
		"__aish_test 'What is G here'\n" +
		"__aish_test 'ls G'\n" +
		"\\setopt ignorebraces\n" +
		"for o in " + opts + "; do [[ -o $o ]] || \\print -rn -- \"off:$o \"; done; \\print -rn -- $'\\x1f'\n" +
		"\\print -rn -- \"${#aliases[@]} ${#galiases[@]}\"$'\\x1f'\n" +
		"for p in \"${(@k)parameters[@]}\"; do [[ $p == __aish_* || $before == *\" $p \"* || \" o p before BUFFER PREBUFFER CURSOR zle_bracketed_paste precmd_functions preexec_functions zshaddhistory_functions \" == *\" $p \"* ]] || \\print -rn -- \"new:$p \"; done; \\print -rn -- $'\\x1f'\n"
	got, _ := zshRun(t, "", rc, script)
	want := []string{`__aish_ask What\ is\ G\ here`, "ls G", "", "7 2", ""}
	for i, w := range want {
		if at(got, i) != w {
			t.Errorf("%d: %q, want %q (all %q)", i, at(got, i), w, got)
		}
	}
	for _, s := range got {
		if strings.Contains(s, "parameter") || strings.Contains(s, "SETOPT") || strings.Contains(s, "UNSET") || strings.Contains(s, "LOCAL") {
			t.Errorf("init.zsh warned or ran an alias: %q", s)
		}
	}
}
