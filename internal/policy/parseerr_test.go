package policy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cedar-go names the file only of the policies it parsed; a syntax error
// says <input>, and with several files and directories the user would not
// know which one to fix.
func TestParseErrorNamesFile(t *testing.T) {
	for name, tc := range map[string]struct{ src, line string }{
		"unclosed paren": {
			src:  permitAll + `forbid(principal, action == Action::"run", resource when { context.program == "rm" };` + "\n",
			line: ":2:",
		},
		"unterminated string": {
			src:  permitAll + "\n" + `forbid(principal, action, resource) when { context.program == "rm };` + "\n",
			line: ":3:",
		},
	} {
		dir := t.TempDir()
		bad := filepath.Join(dir, "bad.cedar")
		if err := os.WriteFile(filepath.Join(dir, "good.cedar"), []byte(permitAll), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(bad, []byte(tc.src), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Load(context.Background(), dir, Rules{})
		if err == nil {
			t.Errorf("%s: loaded", name)
			continue
		}
		msg := err.Error()
		if !strings.Contains(msg, bad+tc.line) || strings.Contains(msg, "<input>") {
			t.Errorf("%s: want %s%s and no <input>: %v", name, bad, tc.line, err)
		}
	}
}
