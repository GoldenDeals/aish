package policy

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestExampleCommands runs bash commands through examples/policy, which
// users copy as is. The reason is checked along with the verdict: a deny
// from an evaluation error would otherwise pass for the rule that should
// have fired.
func TestExampleCommands(t *testing.T) {
	ctx := context.Background()
	e, err := Load(ctx, filepath.Join("..", "..", "examples", "policy"), Rules{})
	if err != nil {
		t.Fatal(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	const (
		rm    = "recursive delete of / or $HOME"
		force = "force push is not allowed"
	)
	for _, c := range []struct{ cmd, want, reason string }{
		{"rm -rf ~", Deny, rm},
		{"rm --recursive --force ~", Deny, rm},
		{"rm -r -f /", Deny, rm},
		{"git push -f", Deny, force},
		{"git push --force origin main", Deny, force},
		{"git push origin +main", Deny, force},
		{"git push origin +HEAD:main", Deny, force},
		{"sudo ls", Deny, "sudo is not allowed for the agent"},
		{"rm -rf ./build", Allow, ""},
		{"git push origin main", Allow, ""},
		{"ls", Allow, ""},
		{"echo 'x", Ask, "could not parse the command"},
	} {
		d, err := e.Check(ctx, callInput("bash", map[string]any{"command": c.cmd}, home))
		if err != nil {
			t.Fatal(err)
		}
		if d.Action != c.want || d.Reason != c.reason {
			t.Errorf("%s: %s (%s), want %s (%s)", c.cmd, d.Action, d.Reason, c.want, c.reason)
		}
	}
}
