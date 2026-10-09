package policy

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Guard is the verdict of the guard alone: what the policies and the
// rules deny goes, trusting a project does not, in any engine, a nil one
// and a subagent's too.
func TestEngineGuard(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	dir := t.TempDir()
	src := "permit(principal, action, resource);\nforbid(principal, action == Action::\"run\", resource == Command::\"sudo\");\n"
	if err := os.WriteFile(filepath.Join(dir, "p.cedar"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	strict, err := Load(ctx, dir, Rules{Deny: []string{"rm *"}, Ask: []string{"git push*"}, WriteOutsideHome: Deny})
	if err != nil {
		t.Fatal(err)
	}
	for name, e := range map[string]*Engine{"strict": strict, "subagent": strict.Subagent("alpha"), "nil": nil} {
		for _, c := range []struct{ cmd, want string }{
			{"rm -rf x", Allow},
			{"sudo ls", Allow},
			{"git push", Allow},
			{"echo x > /etc/x", Allow},
			{"aish trust", Deny},
			{"cd /tmp && aish trust", Deny},
			{"echo '{}' > ~/.local/share/aish/trusted.json", Deny},
			{`eval "aish trust"`, Deny},
		} {
			d, err := e.Guard(ctx, callInput("bash", map[string]any{"command": c.cmd}, home))
			if err != nil {
				t.Fatal(err)
			}
			if d.Action != c.want || c.want == Deny && d.Reason != TrustReason {
				t.Errorf("%s: %s: %s (%s), want %s", name, c.cmd, d.Action, d.Reason, c.want)
			}
		}
		d, err := e.Guard(ctx, NewInput("write_file", map[string]any{"path": "~/.local/share/aish/trusted.json", "content": "{}"}, home, nil))
		if err != nil {
			t.Fatal(err)
		}
		if d.Action != Deny {
			t.Errorf("%s: write_file trusted.json: %s", name, d.Action)
		}
	}
	// Check, as aish policy and the agent with yolo off ask, still denies.
	d, err := strict.Check(ctx, callInput("bash", map[string]any{"command": "rm -rf x"}, home))
	if err != nil || d.Action != Deny {
		t.Errorf("Check rm -rf x: %s, %v", d.Action, err)
	}
}
