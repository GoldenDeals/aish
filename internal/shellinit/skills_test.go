package shellinit

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestRouteSkills checks that a skill's name as the first word sends the
// line to the assistant as `/name args`: a personal skill from ~/.claude,
// a project one from a parent directory's .claude, and the /name form. A
// function of the same name wins, as does anything that is not a bare name.
func TestRouteSkills(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "proj", "sub")
	skill := "---\nname: x\ndescription: y\n---\nDo it.\n"
	files := map[string]string{
		filepath.Join(dir, "init.bash"):                                              Bash,
		filepath.Join(dir, ".claude", "skills", "fix-issue", "SKILL.md"):             skill,
		filepath.Join(dir, ".claude", "skills", "myfn", "SKILL.md"):                  skill,
		filepath.Join(dir, "proj", ".claude", "skills", "release-notes", "SKILL.md"): skill,
		filepath.Join(dir, "xdg", "aish", "skills", "deploy", "SKILL.md"):            skill,
	}
	for p, s := range files {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ in, want string }{
		{"fix-issue 123", "__aish_ask '/fix-issue 123'"},
		{"  fix-issue", "__aish_ask '/fix-issue'"},
		{`release-notes v2 "x y"`, `__aish_ask '/release-notes v2 "x y"'`}, // project skill from a parent directory
		{"/release-notes v2", "__aish_ask '/release-notes v2'"},
		{"deploy now", "__aish_ask '/deploy now'"}, // $XDG_CONFIG_HOME/aish/skills
		{"/usr/bin/env", "/usr/bin/env"},
		{"/nope", "/nope"},
		{"myfn 1", "myfn 1"}, // a function of the same name wins
		{"fix-issue;ls", "fix-issue;ls"},
		{"fix-issue|ls", "fix-issue|ls"},
		{"nope 1", "nope 1"},
		{"Fix-issue 123", "__aish_ask 'Fix-issue 123'"},
	}
	var script strings.Builder
	script.WriteString("myfn() { :; }\nsource " + filepath.Join(dir, "init.bash") + " 2>/dev/null\n")
	for _, c := range cases {
		script.WriteString("__aish_fresh=1; READLINE_LINE=" + quote(c.in) + "; __aish_route; printf '%s\\x1f' \"$READLINE_LINE\"\n")
	}
	cmd := exec.Command("bash", "--norc", "--noprofile", "-i")
	cmd.Dir = proj
	cmd.Stdin = strings.NewReader(script.String())
	cmd.Env = append(os.Environ(), "PS1=", "HISTFILE=/dev/null", "LC_ALL=C.UTF-8",
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
