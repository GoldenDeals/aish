package policy

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/GoldenDeals/aish/internal/config"
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
	trust := filepath.Join(data, "trusted.json")
	if got := config.TrustFile(); got != trust {
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
		{"ls ~/.local/share/aish/sessions", Allow},

		// A redirection writes the file no operand names.
		{"echo x > ~/.local/share/aish/trusted.json", Deny},
		{"cat a >> " + trust, Deny},
		{"echo '{}' >| ~/link/trusted.json", Deny},
		{"{ echo x; } &> $HOME/.local/share/aish/trusted.json", Deny},
		{"sh -c 'echo x > ~/.local/share/aish/trusted.json'", Deny},
		{"echo x > /tmp/y", Allow},
		{"echo x > ~/notes.txt 2>&1", Allow},

		// A tree copied into a directory above it brings a trusted.json
		// of its own; moved away and back, the directory does too.
		{"cp -r x ~/.local/share/", Deny},
		{"rsync -a x/ ~/.local", Deny},
		{"tar -xf x.tar -C ~/.local/share", Deny},
		{"tar xzf x.tgz -C ~/.local", Deny},
		{`find /tmp/x -exec cp -r {} ~/.local/share \;`, Deny},
		{"mv ~/.local/share /tmp/s", Deny},
		{"cd ~/.local/share && cp -r /tmp/aish .", Deny},
		{"env -C ~/.local tar -x -f /tmp/x.tar", Deny},
		{"cp -r --target-directory=$HOME/.local/share x", Deny},
		{"dd if=/tmp/x of=~/.local/share/aish/trusted.json", Deny},
		{"tar -czf /tmp/b.tgz ~/.local/share", Allow},
		{"cp ~/.local/share/x.txt /tmp/", Allow},
		{"cp -r x ~/", Allow},
		{"make install PREFIX=~/.local", Allow},
		{"pip install --prefix ~/.local x", Allow},
		{"find . -exec install -D {} ~/.local/share \\;", Deny},
		{"docker cp box:/x ~/.local/share", Deny},
		{"ls ~/.local/share", Allow},
		{"cd ~/.local/share && ls", Allow},

		// A glob or an expansion that may come to the file or its directory.
		{"cp /tmp/x ~/.local/share/aish/trust*", Deny},
		{"cp /tmp/x ~/.local/sh?re/aish/trusted.json", Deny},
		{`cp /tmp/x ~/.local/share/aish/"$n"`, Deny},
		{"cp -r x ~/.local/sha*", Deny},
		{"cat ~/.local/share/aish/sessions/*.jsonl", Allow},
		{"grep -n trusted.json internal/*/*.go", Allow},

		// What the guard cannot see into is denied when it names the file
		// or aish trust.
		{"aish trust 'x", Deny},
		{"aish trust .\necho 'x", Deny},
		{`bash -c 'aish trust "x'`, Deny},
		{"TRUST=~/.local/share/aish/trusted.json; cat a >> $TRUST", Deny},
		{"cd ~/.local/share && echo x > aish/trusted.json", Deny},
		{`f=~/.local/share/aish/trusted.json; cp /tmp/x "$f"`, Deny},
		{"cp /tmp/x ~/.local/share/{aish,x}/trusted.json", Deny},
		{"echo 'aish trust .' | bash", Deny},
		{"PROMPT_COMMAND='aish trust .'", Deny},
		{"echo 'x", Allow},
		{`echo x > "$f"`, Allow},
		{`grep -rn 'aish trust' "$HOME/src"`, Allow},
	}
	// Run where an earlier call has gone with cd.
	cwds := []struct{ cwd, cmd, want string }{
		{filepath.Join(home, ".local", "share"), "cd aish", Deny},
		{data, "cp /tmp/x trusted.json", Deny},
		{data, "cat sessions/x.jsonl", Allow},
		{filepath.Join(home, ".local"), "tar -xf /tmp/x.tar", Deny},
		{filepath.Join(home, ".local"), "cp -r /tmp/share .", Deny},
		{filepath.Join(home, ".local"), "ls share", Allow},
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
		for _, c := range cwds {
			d, err := e.Check(ctx, callInput("bash", map[string]any{"command": c.cmd}, c.cwd))
			if err != nil {
				t.Fatal(err)
			}
			if d.Action != c.want {
				t.Errorf("%s: in %s: %s: %s (%s), want %s", filepath.Base(dir), c.cwd, c.cmd, d.Action, d.Reason, c.want)
			}
		}
		for _, c := range files {
			d, err := e.Check(ctx, NewInput(c.tool, map[string]any{"path": c.path, "content": "{}"}, home, nil))
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
	d, err := e.Check(ctx, NewInput("write_file", map[string]any{"path": filepath.Join(xdg, "aish", "trusted.json")}, home, nil))
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != Deny {
		t.Errorf("write_file in XDG_DATA_HOME: %s", d.Action)
	}
}
