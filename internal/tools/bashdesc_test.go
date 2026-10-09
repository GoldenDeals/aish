package tools

import (
	"strings"
	"testing"
)

// bashTool is bash among the builtins.
func bashTool(t *testing.T) Tool {
	t.Helper()
	for _, tool := range Builtins() {
		if tool.Name() == Bash {
			return tool
		}
	}
	t.Fatal("no bash among the builtins")
	return nil
}

// TestBashDescCd checks that bash's description gives the same rule about
// cd as the system prompt: the model sees both side by side.
func TestBashDescCd(t *testing.T) {
	desc := bashTool(t).Desc()
	for _, want := range []string{"cd -", "(cd dir && make)", "Never cd into the directory the shell is already in"} {
		if !strings.Contains(desc, want) {
			t.Errorf("bash description lacks %q:\n%s", want, desc)
		}
	}
}

// TestBashDescShell checks that bash's description names the user's shell,
// not bash: with shell = "zsh" the tool, still called bash, runs its
// commands in zsh, as the Environment section of the prompt says.
func TestBashDescShell(t *testing.T) {
	tool := bashTool(t)
	desc := tool.Desc()
	if strings.Contains(strings.ToLower(desc), "bash session") {
		t.Errorf("bash description speaks of a bash session:\n%s", desc)
	}
	for _, want := range []string{"the user's interactive shell (bash or zsh, see Environment)", "/dev/null", "`return`"} {
		if !strings.Contains(desc, want) {
			t.Errorf("bash description lacks %q:\n%s", want, desc)
		}
	}
	for _, a := range tool.Args() {
		if strings.Contains(strings.ToLower(a.Desc), "bash") {
			t.Errorf("argument %s of bash says bash: %q", a.Name, a.Desc)
		}
	}
}
