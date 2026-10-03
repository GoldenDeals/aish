package policy

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/inebotov/aish/internal/config"
)

// The agent cannot trust a project itself, by aish trust or by writing
// trusted.json, whatever the policies say: there are none here, and a
// permit for everything does not lift it either.
func TestGuard(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	data := filepath.Join(home, ".local", "share", "aish")
	if err := os.MkdirAll(filepath.Join(data, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(data, filepath.Join(home, "link")); err != nil {
		t.Fatal(err)
	}
	if got := config.TrustFile(); got != filepath.Join(data, "trusted.json") {
		t.Fatalf("TrustFile: %s", got)
	}
	permit := filepath.Join(root, "permit")
	if err := os.Mkdir(permit, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(permit, "all.cedar"), []byte("permit(principal, action, resource);\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	bash := []struct{ cmd, want string }{
		{"aish trust", Deny},
		{"/usr/local/bin/aish trust .", Deny},
		{"aish trust --revoke", Deny},
		{"aish trust --list", Allow},
		{"ls", Allow},
		{"aish status", Allow},
		{`$AISH_BIN trust`, Deny},
		{`"${AISH_BIN}" trust`, Deny},
		{"AISH_SOCK= aish trust", Deny},
		{"cd /tmp && sudo -u me env -u AISH_SOCK aish trust", Deny},
		{"bash -c 'cd /x && aish trust'", Deny},
		{`eval "aish trust"`, Deny},
		{"alias t='aish trust'", Deny},
		{"bash <<'EOF'\naish trust\nEOF", Deny},
		{`find . -maxdepth 0 -exec aish trust \;`, Deny},
		{`aish "$(echo trust)"`, Deny},
		{"aish tru?t", Deny},
		{`"$(command -v aish)" trust`, Deny},
		{`go build -o aish ./cmd/aish && cp aish "$HOME/bin/"`, Allow},
		{`grep -rn 'aish trust' .`, Allow},
		{"rm ~/.local/share/aish/trusted.json", Deny},
		{"cp /tmp/x $HOME/.local/share/aish/trusted.json", Deny},
		{"cp /tmp/x ~/link/trusted.json", Deny},
		{"rm -rf ~/.local/share/aish/", Deny},
		{"mv /tmp/aish ~/.local/share/aish", Deny},
		{"ln -s ~/.local/share/aish/trusted.json /tmp/t", Deny},
		{"cat ~/.local/share/aish/sessions/x.jsonl", Allow},
	}
	files := []struct {
		tool, path, want string
	}{
		{"write_file", filepath.Join(data, "trusted.json"), Deny},
		{"write_file", "~/.local/share/aish/trusted.json", Deny},
		{"edit_file", "link/trusted.json", Deny},
		{"write_file", "~/notes.txt", Allow},
		{"read_file", "~/.local/share/aish/trusted.json", Allow},
	}
	for _, dir := range []string{t.TempDir(), permit} {
		e, err := Load(ctx, dir, Rules{})
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range bash {
			d, err := e.Check(ctx, callInput("bash", map[string]any{"command": c.cmd}, home))
			if err != nil {
				t.Fatal(err)
			}
			if d.Action != c.want {
				t.Errorf("%s: %s: %s (%s), want %s", filepath.Base(dir), c.cmd, d.Action, d.Reason, c.want)
			}
			if d.Action == Deny && d.Reason != TrustReason {
				t.Errorf("%s: reason %q", c.cmd, d.Reason)
			}
		}
		for _, c := range files {
			d, err := e.Check(ctx, NewInput(c.tool, map[string]any{"path": c.path, "content": "{}"}, home))
			if err != nil {
				t.Fatal(err)
			}
			if d.Action != c.want {
				t.Errorf("%s: %s %s: %s (%s), want %s", filepath.Base(dir), c.tool, c.path, d.Action, d.Reason, c.want)
			}
		}
	}

	// trusted.json of XDG_DATA_HOME, as aish trust writes it there.
	xdg := filepath.Join(root, "xdg")
	t.Setenv("XDG_DATA_HOME", xdg)
	e, err := Load(ctx, t.TempDir(), Rules{})
	if err != nil {
		t.Fatal(err)
	}
	d, err := e.Check(ctx, NewInput("write_file", map[string]any{"path": filepath.Join(xdg, "aish", "trusted.json")}, home))
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != Deny {
		t.Errorf("write_file in XDG_DATA_HOME: %s", d.Action)
	}
}
