package tools

import (
	"strings"
	"testing"
)

// TestBashDescCd checks that bash's description gives the same rule about
// cd as the system prompt: the model sees both side by side.
func TestBashDescCd(t *testing.T) {
	var desc string
	for _, tool := range Builtins() {
		if tool.Name() == Bash {
			desc = tool.Desc()
		}
	}
	if desc == "" {
		t.Fatal("no bash among the builtins")
	}
	for _, want := range []string{"cd -", "(cd dir && make)", "Never cd into the directory the shell is already in"} {
		if !strings.Contains(desc, want) {
			t.Errorf("bash description lacks %q:\n%s", want, desc)
		}
	}
}
