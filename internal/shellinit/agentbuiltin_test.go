package shellinit

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/subagent"
)

// builtinAgents are the names of the subagents subagent.Find gives without
// a file: init.bash and init.zsh have them written out.
func builtinAgents(t *testing.T) []string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	defs, _ := subagent.Find("")
	var names []string
	for _, d := range defs {
		if d.Builtin {
			names = append(names, d.Name)
		}
	}
	if len(names) == 0 {
		t.Fatal("no subagents of aish's own")
	}
	return names
}

// TestBuiltinAgentsBash: __aish_is_agent and __aish_comp_agent of init.bash
// know aish's own subagents with no file anywhere, by their names as they
// are: explore is not Explore.
func TestBuiltinAgentsBash(t *testing.T) {
	names := builtinAgents(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "init.bash"), []byte(Bash), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "source " + filepath.Join(dir, "init.bash") + " 2>/dev/null\n" +
		"for n in " + strings.Join(names, " ") + " explore nope; do __aish_is_agent \"$n\" && printf '%s\\x1f' \"$n\"; done\n" +
		"printf '\\x1e'; __aish_comp_agent '' && printf '%s\\x1f' \"${COMPREPLY[@]}\"\n"
	cmd := exec.Command("bash", "--norc", "--noprofile", "-i")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = cleanEnv("PS1=", "HISTFILE=/dev/null", "LC_ALL=C.UTF-8", "HOME="+dir, "XDG_CONFIG_HOME="+filepath.Join(dir, "xdg"))
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	is, comp, _ := strings.Cut(regexp.MustCompile("\x1b]6973;[^\a]*\a").ReplaceAllString(string(out), ""), "\x1e")
	if got := strings.Split(strings.TrimSuffix(is, "\x1f"), "\x1f"); !slices.Equal(got, names) {
		t.Errorf("__aish_is_agent: %q, want %q", got, names)
	}
	got := strings.Split(strings.TrimSuffix(comp, "\x1f"), "\x1f")
	slices.Sort(got)
	if !slices.Equal(got, names) {
		t.Errorf("__aish_comp_agent: %q, want %q", got, names)
	}
}

// TestBuiltinAgentsZsh: __aish_is_agent of init.zsh, as in bash; its
// completion adds the names as they are.
func TestBuiltinAgentsZsh(t *testing.T) {
	names := builtinAgents(t)
	script := "for n in " + strings.Join(names, " ") + " explore nope; do __aish_is_agent $n && \\print -rn -- \"$n\"$'\\x1f'; done; :\n"
	got, _ := zshRun(t, "", "", script)
	if got = got[:len(got)-1]; !slices.Equal(got, names) {
		t.Errorf("__aish_is_agent: %q, want %q", got, names)
	}
	m := regexp.MustCompile(`\n\tnames\+=\(([^)]*)\)\n`).FindStringSubmatch(Zsh)
	if m == nil {
		t.Fatal("__aish_comp_agent adds no names")
	}
	added := strings.Fields(m[1])
	slices.Sort(added)
	if !slices.Equal(added, names) {
		t.Errorf("__aish_comp_agent adds %q, want %q", added, names)
	}
}
