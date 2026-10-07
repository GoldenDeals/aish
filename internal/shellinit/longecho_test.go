package shellinit

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// printLine prints the line __aish_route left, then \x1f: a request as
// `__aish_ask '<text>'`, the text quoted from __aish_req, any other line as
// it is.
const printLine = `if [[ $READLINE_LINE == '__aish_ask "$__aish_req"' ]]; then printf '__aish_ask %s\x1f' "${__aish_req@Q}"; else printf '%s\x1f' "$READLINE_LINE"; fi`

// TestLongEcho types two requests at a prompt, as TestExpandStatus does,
// and plays all bash printed, readline's echo and redraws with it, on a
// screen that keeps a history, as tmux does. A request three screens long
// leaves no row of `__aish_ask` on the screen or in its history: what
// readline draws before accept-line is a short line that reads the text
// from __aish_req. A paste that ends in a newline leaves no empty row under
// the request, and the model gets it without the newline. The agent's
// command has the text as $1 and no __aish_req, nor does the shell's state
// after the request; history has what was typed.
func TestLongEcho(t *testing.T) {
	const cols, rows = 40, 10
	var words []string
	for n := 0; n < 3*cols*rows; n += len(words[len(words)-1]) + 1 {
		words = append(words, fmt.Sprintf("word%d", len(words)))
	}
	long := "Explain " + strings.Join(words, " ")

	dir := t.TempDir()
	run := filepath.Join(dir, "run")
	init := filepath.Join(dir, "init.bash")
	files := map[string]string{
		init:                        Bash,
		filepath.Join(run, "nonce"): "N\n",
		filepath.Join(run, "route"): "",
	}
	for p, s := range files {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// `agent start` records the text and hands the shell a command that
	// records its $1 and __aish_req; `agent resume` ends the request. C-v
	// C-j puts a newline in the line, as a paste does.
	script := `AISH_BIN=fake
agent='printf "%s\x1f%s\x1f" "$1" "${__aish_req-unset}" >>args'
fake() { [[ $2 == start ]] || return 0; printf '%s\x1f' "$4" >>sent; printf id >"$AISH_RUN/next.id"; printf %s "$agent" >"$AISH_RUN/next.cmd"; }
source ` + init + ` 2>/dev/null
` + long + "\nText\x16\n\nhistory >hist\n"
	cmd := exec.Command("bash", "--norc", "--noprofile", "-i")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(script)
	// Readline draws on stderr; one pipe for both keeps the order.
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.Env = cleanEnv("PS1=> ", "PS2=", "HISTFILE=/dev/null", "LC_ALL=C.UTF-8", "TERM=xterm",
		fmt.Sprintf("COLUMNS=%d", cols), fmt.Sprintf("LINES=%d", rows),
		"HOME="+dir, "XDG_CONFIG_HOME="+filepath.Join(dir, "xdg"), "AISH_RUN="+run)
	if err := cmd.Run(); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	s := newScreen(cols, rows)
	s.write(t, regexp.MustCompile("\x1b]6973;[^\a]*\a").ReplaceAllString(out.String(), ""))
	shown := append(append([]string(nil), s.history...), s.lines()...)
	for _, row := range shown {
		if strings.Contains(row, "__aish_ask") {
			t.Errorf("the echo stayed: %q", row)
		}
	}
	text := strings.Join(shown, "\n") + "\n"
	if asked := strings.Join(rowsOf("> ? "+long, cols), "\n") + "\n"; !strings.Contains(text, asked) {
		t.Errorf("the long request is not on the screen:\n%s", text)
	}
	if !strings.Contains(text, "\n> ? Text\n> history >hist\n") {
		t.Errorf("want the pasted request with no empty row under it:\n%s", text)
	}

	read := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if got, want := read("sent"), long+"\x1fText\x1f"; got != want {
		t.Errorf("sent %q, want %q", got, want)
	}
	if got, want := read("args"), long+"\x1funset\x1fText\x1funset\x1f"; got != want {
		t.Errorf("the agent's command got $1 and __aish_req %q, want %q", got, want)
	}
	// The functions that name it are in the state too.
	if regexp.MustCompile(`(?m)^declare -\S* __aish_req=`).MatchString(read("run/state")) {
		t.Errorf("__aish_req stayed in the shell's state")
	}
	hist := strings.Split(regexp.MustCompile(`(?m)^\s*[0-9]+\s+`).ReplaceAllString(read("hist"), ""), "\n")
	if want := []string{long, "Text", "history >hist", ""}; len(hist) < len(want) ||
		strings.Join(hist[len(hist)-len(want):], "\n") != strings.Join(want, "\n") {
		t.Errorf("history %q, want it to end with %q", hist, want)
	}
}
