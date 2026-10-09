package shellinit

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// agentFiles makes, in the home, the subagent spec-writer of ~/.claude, a
// README beside it that is no subagent, and in proj, where the shell runs,
// code-reviewer of proj/.claude.
const agentFiles = `mkdir -p .claude/agents proj/.claude/agents other
printf -- '---\nname: spec-writer\ndescription: Runs\n---\nRun.\n' >.claude/agents/spec-writer.md
printf '# Agents\n' >.claude/agents/README.md
printf -- '---\nname: code-reviewer\ndescription: Reviews\n---\nReview.\n' >proj/.claude/agents/code-reviewer.md
`

// spawnCases are lines routed as Enter routes them, and what they become:
// the subagent's name, a bar, and the line as printLine prints it.
var spawnCases = []struct{ in, want string }{
	{"&code-reviewer check x", "code-reviewer|__aish_ask 'check x'"},
	{"  &code-reviewer   check  x  ", "code-reviewer|__aish_ask 'check  x'"},
	{"&code-reviewer check\nthe rest", "code-reviewer|__aish_ask $'check\\nthe rest'"},
	{"&nope x", "nope|__aish_ask 'x'"}, // __aish_spawning tells there is none
	{"&code-reviewer", "code-reviewer|__aish_ask ''"},
	{"&>f ls", "|&>f ls"},
	{"&& ls", "|&& ls"},
	{"& ls", "|& ls"},
	{"?&code-reviewer x", "|__aish_ask '&code-reviewer x'"},
	{"Explain &code-reviewer", "|__aish_ask 'Explain &code-reviewer'"},
}

// TestRouteSpawn: a line "&NAME text" goes to __aish_ask with NAME for
// __aish_spawning; & and anything but a name stays bash's.
func TestRouteSpawn(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "init.bash"), []byte(Bash), 0o644); err != nil {
		t.Fatal(err)
	}
	var script strings.Builder
	script.WriteString("source " + filepath.Join(dir, "init.bash") + " 2>/dev/null\n")
	for _, c := range spawnCases {
		script.WriteString("__aish_fresh=1; READLINE_LINE=" + bashQuoted(c.in) + "; __aish_route; printf '%s|' \"$__aish_spawn\"; " + printLine + "\n")
	}
	cmd := exec.Command("bash", "--norc", "--noprofile", "-i")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(script.String())
	cmd.Env = cleanEnv("PS1=", "HISTFILE=/dev/null", "LC_ALL=C.UTF-8", "HOME="+dir)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(regexp.MustCompile("\x1b]6973;[^\a]*\a").ReplaceAllString(string(out), ""), "\x1f")
	for i, c := range spawnCases {
		if at(got, i) != c.want {
			t.Errorf("%q -> %q, want %q", c.in, at(got, i), c.want)
		}
	}
}

// bashQuoted is s as $'...', newlines and all.
func bashQuoted(s string) string {
	return "$'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`, "\n", `\n`).Replace(s) + "'"
}

// TestSpawnTyped types "&NAME text" at a bash prompt: aish agent spawn gets
// NAME and the text, of a file's subagent or of aish's own, which has no
// file, and no request starts (ask-start); a name with no subagent, a
// README's, one of aish's own in another case, or no text gets a hint and
// calls nothing. History has the lines as typed.
func TestSpawnTyped(t *testing.T) {
	rc := agentFiles + "printf '#!/bin/sh\\nprintf \"%%s\\\\n\" \"$*\" >>\"$HOME/called\"\\n' >stub; chmod +x stub\n" +
		"AISH_BIN=$HOME/stub\ncd proj\n"
	dir, out := typed(t, "", rc, ": >../ready\r",
		"&code-reviewer check x\r", "&nope x\r", "&README x\r", "&code-reviewer\r", "&spec-writer  go on \r",
		"&Explore find x\r", "&explore x\r", "&general-purpose do y\r", "history >../hist\r")
	called, _ := os.ReadFile(filepath.Join(dir, "called"))
	if want := "agent spawn code-reviewer -- check x\nagent spawn spec-writer -- go on\n" +
		"agent spawn Explore -- find x\nagent spawn general-purpose -- do y\n"; string(called) != want {
		t.Errorf("called %q, want %q", called, want)
	}
	if strings.Contains(out, ";ask-start") {
		t.Errorf("a request started:\n%q", out)
	}
	for _, hint := range []string{"aish: no subagent nope here; aish agents lists them\n",
		"aish: no subagent README here", "aish: what is code-reviewer to do? &code-reviewer TEXT\n",
		"aish: no subagent explore here"} {
		if !strings.Contains(out, hint) {
			t.Errorf("no hint %q:\n%q", hint, out)
		}
	}
	hist, _ := os.ReadFile(filepath.Join(dir, "hist"))
	for _, line := range []string{"&code-reviewer check x", "&nope x", "&code-reviewer", "&spec-writer  go on", "&Explore find x"} {
		if !regexp.MustCompile(`(?m)^ *\d+ +` + regexp.QuoteMeta(line) + `$`).Match(hist) {
			t.Errorf("history has no %q:\n%s", line, hist)
		}
	}
	if strings.Contains(string(hist), "__aish_ask") {
		t.Errorf("history has the line rewritten:\n%s", hist)
	}
}

// TestZshRouteSpawn: the route of init.bash, in zsh.
func TestZshRouteSpawn(t *testing.T) {
	const line = `__aish_tests() { \setopt localoptions nowarncreateglobal nowarnnestedvar; BUFFER=$1; PREBUFFER=; __aish_route; ` +
		`if [[ -n $__aish_t || -n $__aish_raw || -n $__aish_spawn ]]; then __aish_request; fi; __aish_t= __aish_raw=; \print -rn -- "$__aish_spawn|"; ` +
		`if [[ $BUFFER == '__aish_ask "$__aish_req"' ]]; then \print -rn -- "__aish_ask ${(qq)__aish_req}"$'\x1f'; else \print -rn -- "$BUFFER"$'\x1f'; fi; }` + "\n"
	var script strings.Builder
	script.WriteString(line)
	for _, c := range spawnCases {
		script.WriteString("__aish_tests " + bashQuoted(c.in) + "\n")
	}
	got, _ := zshRun(t, "", "", script.String())
	for i, c := range spawnCases {
		want := strings.Replace(c.want, `$'check\nthe rest'`, "'check\nthe rest'", 1)
		if at(got, i) != want {
			t.Errorf("%q -> %q, want %q", c.in, at(got, i), want)
		}
	}
}

// TestZshSpawnTyped: TestSpawnTyped in zsh, Enter and all.
func TestZshSpawnTyped(t *testing.T) {
	steps := []zstep{{prompts: 1}}
	for i, keys := range []string{"&code-reviewer check x\r", "&nope x\r", "&README x\r", "&code-reviewer\r", "&spec-writer  go on \r",
		"&Explore find x\r", "&explore x\r", "&general-purpose do y\r", "fc -ln 1 >../hist\r"} {
		steps = append(steps, zstep{prompts: i + 1, keys: keys})
	}
	steps = append(steps, zstep{file: "hist"})
	rc := agentFiles + "cd proj\n"
	dir, out := zshTyped(t, rc, steps...)
	called, _ := os.ReadFile(filepath.Join(dir, "called"))
	if want := "agent spawn code-reviewer -- check x\nagent spawn spec-writer -- go on\n" +
		"agent spawn Explore -- find x\nagent spawn general-purpose -- do y\n"; string(called) != want {
		t.Errorf("called %q, want %q", called, want)
	}
	if strings.Contains(out, ";ask-start") {
		t.Errorf("a request started:\n%q", out)
	}
	for _, hint := range []string{"aish: no subagent nope here; aish agents lists them",
		"aish: no subagent README here", "aish: what is code-reviewer to do? &code-reviewer TEXT",
		"aish: no subagent explore here"} {
		if !strings.Contains(out, hint) {
			t.Errorf("no hint %q:\n%q", hint, out)
		}
	}
	hist, _ := os.ReadFile(filepath.Join(dir, "hist"))
	for _, line := range []string{"&code-reviewer check x", "&nope x", "&code-reviewer", "&spec-writer  go on", "&Explore find x"} {
		if !regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(line) + `$`).Match(hist) {
			t.Errorf("history has no %q:\n%s", line, hist)
		}
	}
}

// spawnTabCases are those both shells complete alike: the subagents after
// an & that starts the line, of this directory and the home's, not the
// README, and aish's own; @path after the name, as in a request.
var spawnTabCases = []tabCase{
	{"&code-\t", "&code-reviewer "},
	{"&spec-w\t", "&spec-writer "},
	{"&Expl\t", "&Explore "},
	{"&general\t", "&general-purpose "},
	{"&README\t", "&README"},
	{"&code-reviewer @ma\t", "&code-reviewer @main.go "},
}

// TestCompleteAgentBash: Tab after & in bash. Bash gives the word after &
// as the first of a command, the & left out: an empty one gets the
// subagents, and so does one no command starts; one that starts a command
// stays the command's, as at the start of a line.
func TestCompleteAgentBash(t *testing.T) {
	rc := strings.Replace(compHome, "cd proj\n", "", 1) + agentFiles + "cd proj\nAISH_BIN=$HOME/stub\n" +
		`bind -x '"\C-xd": printf "%s\n" "$READLINE_LINE" >>"$HOME/line"; READLINE_LINE='` + "\n"
	cases := append([]tabCase{
		{"&\t", "&"},                  // five of them
		{"&ech\t", "&echo "},          // a command's start: the subagent echoer is not offered
		{"code-\t", "code-reviewer "}, // bash does not see that no & is there
	}, spawnTabCases...)
	keys := []string{": >../ready\r"}
	for _, c := range cases {
		keys = append(keys, c.keys+"\x18d")
	}
	keys = append(keys, "cd ../other\r", "&spec-\t\x18d", "&code-\t\x18d")
	cases = append(cases, tabCase{"&spec-\t", "&spec-writer "}, tabCase{"&code-\t", "&code-"}) // the home's alone
	dir, _ := typed(t, "", rc+"printf -- '---\\nname: echoer\\ndescription: E\\n---\\nE.\\n' >.claude/agents/echoer.md\n", keys...)
	lines, _ := os.ReadFile(filepath.Join(dir, "line"))
	got := strings.Split(string(lines), "\n")
	for i, c := range cases {
		if compAt(got, i) != c.want {
			t.Errorf("%q: %q, want %q", c.keys, compAt(got, i), c.want)
		}
	}
}

// TestCompleteAgentZsh: Tab after & in zsh, which shows the line before the
// word: only an & that starts the line takes subagents.
func TestCompleteAgentZsh(t *testing.T) {
	zshPath(t)
	rc := strings.Replace(zshComp(true), "cd proj\n", "", 1) + agentFiles + "cd proj\n"
	cases := append([]tabCase{
		{"&\t", "&"}, // four of them
		{"ls &code-\t", "ls &code-"},
	}, spawnTabCases...)
	steps := []zstep{{prompts: 1}}
	for _, c := range cases {
		steps = append(steps, zstep{keys: c.keys + "\x18d"})
	}
	steps = append(steps, zstep{keys: "cd ../other\r"}, zstep{prompts: 2, keys: "&spec-\t\x18d"}, zstep{keys: "&code-\t\x18d"},
		zstep{keys: ": >../done\r"}, zstep{file: "done"})
	cases = append(cases, tabCase{"&spec-\t", "&spec-writer "}, tabCase{"&code-\t", "&code-"}) // the home's alone
	dir, _ := zshTyped(t, rc, steps...)
	lines, _ := os.ReadFile(filepath.Join(dir, "line"))
	got := strings.Split(string(lines), "\n")
	for i, c := range cases {
		if compAt(got, i) != c.want {
			t.Errorf("%q: %q, want %q", c.keys, compAt(got, i), c.want)
		}
	}
}
