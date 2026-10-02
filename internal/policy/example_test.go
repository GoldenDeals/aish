package policy

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestExampleCommands runs tool calls through examples/policy, which users
// copy as is. The reason is checked along with the verdict: a deny from an
// evaluation error would otherwise pass for the rule that should have
// fired.
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
		sudo  = "sudo is not allowed for the agent"
	)
	// arg is the command of bash and the path of the file tools.
	for _, c := range []struct{ tool, arg, want, reason string }{
		{"bash", "rm -rf ~", Deny, rm},
		{"bash", "rm --recursive --force ~", Deny, rm},
		{"bash", "rm -r -f /", Deny, rm},
		{"bash", "git push -f", Deny, force},
		{"bash", "git push --force origin main", Deny, force},
		{"bash", "git push origin +main", Deny, force},
		{"bash", "git push origin +HEAD:main", Deny, force},
		{"bash", `bash -c "git push -f origin main"`, Deny, force},
		{"bash", "sudo ls", Deny, sudo},
		{"bash", "make && sudo make install", Deny, sudo},
		{"bash", "sudo rm -rf /", Deny, sudo + "; " + rm},
		{"bash", "exit", Deny, "the agent must not end or replace the shell"},
		{"bash", "rm -rf ./build", Allow, ""},
		{"bash", "rm -rf build", Allow, ""},
		{"bash", "git push origin main", Allow, ""},
		{"bash", "ls", Allow, ""},
		{"bash", "ls -la", Allow, ""},
		{"bash", "pacman -S ripgrep", Ask, "installs packages"},
		{"bash", "echo 'x", Ask, "could not parse the command"},
		{"write_file", "notes.txt", Allow, ""},
		{"write_file", "/etc/hosts", Deny, "writing outside $HOME"},
		{"read_file", "/etc/hosts", Allow, ""},
	} {
		args := map[string]any{"path": c.arg}
		if c.tool == "bash" {
			args = map[string]any{"command": c.arg}
		}
		d, err := e.Check(ctx, callInput(c.tool, args, home))
		if err != nil {
			t.Fatal(err)
		}
		if d.Action != c.want || d.Reason != c.reason {
			t.Errorf("%s %s: %s (%s), want %s (%s)", c.tool, c.arg, d.Action, d.Reason, c.want, c.reason)
		}
	}
}
