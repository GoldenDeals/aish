package policy

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestMatch(t *testing.T) {
	for _, c := range []struct {
		pat, s string
		want   bool
	}{
		{"sudo *", "sudo cat /etc/x", true}, // * takes spaces and slashes
		{"sudo *", "sudo", false},
		{"sudo *", "xsudo ls", false},
		{"rm -rf /", "rm -rf /", true},
		{"rm -rf /", "rm -rf /tmp/x", false},
		{"rm -rf /*", "rm -rf /tmp/x", true},
		{"git push --force*", "git push --force-with-lease origin", true},
		{"git push*", "git push", true},
		{"git push*", "git pull", false},
		{"a?c", "abc", true},
		{"a?c", "ac", false},
		{"a?c", "abbc", false},
		{"?", "é", true}, // one character, not one byte
		{"??", "€", false},
		{"*??", "€", false},
		{"a*b*c", "a/x b y c", true},
		{"a*b*c", "a/x b y", false},
		{"*c", "abcbc", true},
		{"*", "", true},
		{"**", "anything at all", true},
		{"", "", true},
		{"", "x", false},
		{"[a]", "a", false}, // no classes: brackets are literal
		{"[a]", "[a]", true},
	} {
		if got := match(c.pat, c.s); got != c.want {
			t.Errorf("match(%q, %q) = %v, want %v", c.pat, c.s, got, c.want)
		}
	}
	// Several stars against a long command must not take exponential time.
	if match("*a*a*a*a*a*b", strings.Repeat("a", 20000)) {
		t.Error("matched without a b")
	}
}

func TestRules(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	e, err := Load(ctx, t.TempDir(), Rules{
		Deny:             []string{"sudo *", "rm -rf /"},
		Ask:              []string{"apt install *", "sudo apt *"},
		WriteOutsideHome: Deny,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		tool   string
		args   map[string]any
		want   string
		reason string
	}{
		{"bash", map[string]any{"command": "sudo cat /etc/x"}, Deny, `policy: matches "sudo *"`},
		{"bash", map[string]any{"command": "rm -rf /"}, Deny, `policy: matches "rm -rf /"`},
		{"bash", map[string]any{"command": "rm -rf /tmp/x"}, Allow, ""},
		{"bash", map[string]any{"command": "ls -la"}, Allow, ""},
		{"bash", map[string]any{"command": "make && echo x | sudo tee /etc/x"}, Deny, `policy: matches "sudo *"`},
		{"bash", map[string]any{"command": `bash -c "sudo ls"`}, Deny, `policy: matches "sudo *"`},
		{"bash", map[string]any{"command": "cd /tmp && apt install ripgrep"}, Ask, `policy: matches "apt install *"`},
		// Matching both lists, the command is denied and the question is moot.
		{"bash", map[string]any{"command": "sudo apt install ripgrep"}, Deny, `policy: matches "sudo *"`},
		{"bash", map[string]any{"command": "apt install x; sudo ls"}, Deny, `policy: matches "sudo *"`},
		// What does not parse is matched as one command.
		{"bash", map[string]any{"command": "sudo ls 'oops"}, Deny, `policy: matches "sudo *"`},
		{"bash", map[string]any{"command": "echo 'oops"}, Allow, ""},
		{"write_file", map[string]any{"path": "notes.txt"}, Allow, ""},
		{"write_file", map[string]any{"path": filepath.Join(home, "a", "b.txt")}, Allow, ""},
		{"write_file", map[string]any{"path": "/etc/hosts"}, Deny, "policy: writes outside home: " + resolve("/etc/hosts")},
		{"edit_file", map[string]any{"path": "../outside.txt"}, Deny, "policy: writes outside home: " + resolve(filepath.Join(filepath.Dir(home), "outside.txt"))},
		{"read_file", map[string]any{"path": "/etc/hosts"}, Allow, ""},
	} {
		d, err := e.Check(ctx, NewInput(c.tool, c.args, home))
		if err != nil {
			t.Fatal(err)
		}
		if d.Action != c.want || d.Reason != c.reason {
			t.Errorf("%s %v: %s (%s), want %s (%s)", c.tool, c.args, d.Action, d.Reason, c.want, c.reason)
		}
	}
}

func TestRulesWriteOutsideHome(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, c := range []struct{ setting, want string }{
		{"", Allow},
		{Allow, Allow},
		{Ask, Ask},
		{Deny, Deny},
	} {
		e, err := Load(ctx, t.TempDir(), Rules{WriteOutsideHome: c.setting})
		if err != nil {
			t.Fatal(err)
		}
		d, err := e.Check(ctx, NewInput("write_file", map[string]any{"path": "/etc/hosts"}, home))
		if err != nil {
			t.Fatal(err)
		}
		if d.Action != c.want {
			t.Errorf("write_outside_home = %q: %s, want %s", c.setting, d.Action, c.want)
		}
	}
	if _, err := Load(ctx, t.TempDir(), Rules{WriteOutsideHome: "never"}); err == nil {
		t.Error("an unknown write_outside_home loaded")
	}
}

func TestRulesLen(t *testing.T) {
	for _, c := range []struct {
		r    Rules
		want int
	}{
		{Rules{}, 0},
		{Rules{WriteOutsideHome: Allow}, 0},
		{Rules{Deny: []string{"a", "b"}, Ask: []string{"c"}}, 3},
		{Rules{Deny: []string{"a"}, WriteOutsideHome: Ask}, 2},
	} {
		if got := c.r.Len(); got != c.want {
			t.Errorf("%+v: %d, want %d", c.r, got, c.want)
		}
	}
}
