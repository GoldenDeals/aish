package policy

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCommands(t *testing.T) {
	got, err := Commands(`cd /x && sudo -u root r\m -rf "$HOME" | tee log; echo $(git push --force) && bash -c 'exit 3'`)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"cd", "/x"},
		{"sudo", "-u", "root", "rm", "-rf", "$HOME"},
		{"rm", "-rf", "$HOME"},
		{"tee", "log"},
		{"echo", "$(git push --force)"},
		{"git", "push", "--force"},
		{"bash", "-c", "exit 3"},
		{"exit", "3"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	if _, err := Commands("echo 'unterminated"); err == nil {
		t.Fatal("expected a parse error")
	}
}

func TestExamplePolicy(t *testing.T) {
	ctx := context.Background()
	e, err := Load(ctx, filepath.Join("..", "..", "examples", "policy"))
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	for _, c := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"bash", map[string]any{"command": "ls -la"}, Allow},
		{"bash", map[string]any{"command": "make && sudo make install"}, Deny},
		{"bash", map[string]any{"command": "sudo rm -rf /"}, Deny},
		{"bash", map[string]any{"command": "rm -rf build"}, Allow},
		{"bash", map[string]any{"command": `bash -c "git push -f origin main"`}, Deny},
		{"bash", map[string]any{"command": "exit"}, Deny},
		{"bash", map[string]any{"command": "pacman -S ripgrep"}, Ask},
		{"bash", map[string]any{"command": "echo 'oops"}, Ask},
		{"write_file", map[string]any{"path": "notes.txt"}, Allow},
		{"write_file", map[string]any{"path": "/etc/hosts"}, Deny},
		{"read_file", map[string]any{"path": "/etc/hosts"}, Allow},
	} {
		d, err := e.Check(ctx, NewInput(c.tool, c.args, home))
		if err != nil {
			t.Fatal(err)
		}
		if d.Action != c.want {
			t.Errorf("%s %v: %s (%s), want %s", c.tool, c.args, d.Action, d.Reason, c.want)
		}
	}
}

func TestNoPolicies(t *testing.T) {
	e, err := Load(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d, _ := e.Check(context.Background(), NewInput("bash", map[string]any{"command": "sudo x"}, "/"))
	if d.Action != Allow {
		t.Fatal(d)
	}
}

func TestSymlinkOutOfHome(t *testing.T) {
	ctx := context.Background()
	e, err := Load(ctx, filepath.Join("..", "..", "examples", "policy"))
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.Symlink("/etc", filepath.Join(home, "etc")); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ path, want string }{
		{"etc/hosts", Deny},
		{"etc/new/dir/file", Deny}, // does not exist yet
		{"notes.txt", Allow},
		{"new/notes.txt", Allow},
	} {
		in := NewInput("write_file", map[string]any{"path": c.path}, home)
		d, err := e.Check(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		if d.Action != c.want {
			t.Errorf("%s → %s: %s (%s), want %s", c.path, in.Path, d.Action, d.Reason, c.want)
		}
	}
}
