package policy

import (
	"context"
	"testing"
)

// A pattern ending in " *" takes the bare command too: `sudo *` is any
// sudo, the interactive one and the value of `alias s=sudo` included.
// Other patterns match as they did.
func TestMatchBare(t *testing.T) {
	for _, c := range []struct {
		pat, s string
		want   bool
	}{
		{"sudo *", "sudo", true},
		{"sudo *", "sudo ls", true},
		{"sudo *", "sudoedit", false},
		{"sudo *", "sudoedit x", false},
		{"sudo *", "xsudo", false},
		{"sudo -? *", "sudo -s", true},
		{"sudo -? *", "sudo", false},
		{"git push --force *", "git push --force", true},
		{"git push --force*", "git push --force", true},
		{"git push --force*", "git push", false},
		{"sudo*", "sudoedit", true},
		{"sudo?*", "sudo", false},
		{"sudo ?", "sudo", false},
		{"sudo *x", "sudo", false},
	} {
		if got := match(c.pat, c.s); got != c.want {
			t.Errorf("match(%q, %q) = %v, want %v", c.pat, c.s, got, c.want)
		}
	}
}

func TestRulesBare(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	e, err := Load(ctx, t.TempDir(), Rules{
		Deny: []string{"sudo *", "git push --force*"},
		Ask:  []string{"doas *"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ cmd, want, reason string }{
		{"sudo", Deny, `matches "sudo *"`},
		{"sudo ls", Deny, `matches "sudo *"`},
		// The value of an alias is a command of its own: s runs sudo.
		{"alias s=sudo", Deny, `matches "sudo *"`},
		{"alias s='sudo'; s ls", Deny, `matches "sudo *"`},
		{"sudoedit x", Allow, ""},
		{"doas", Ask, `matches "doas *"`},
		{"git push --force", Deny, `matches "git push --force*"`},
		{"git push --force-with-lease origin", Deny, `matches "git push --force*"`},
		{"git push origin", Allow, ""},
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

// write_outside_home cannot tell where a redirection to a file known only
// at run time writes, so it takes it for a write outside home; a program
// built at run time writing to a known file, or to no file, is judged by
// the file.
func TestRulesUnknownWrite(t *testing.T) {
	ctx := context.Background()
	home, _ := links(t, nil)
	t.Setenv("HOME", home)
	const unknown = "writes a file known only at run time"
	for _, c := range []struct {
		rules        Rules
		cmd          string
		want, reason string
	}{
		{Rules{WriteOutsideHome: Ask}, `echo x > "$f"`, Ask, unknown},
		{Rules{WriteOutsideHome: Deny}, `echo x > "$f"`, Deny, unknown},
		{Rules{WriteOutsideHome: Ask}, `echo x >> $(mktemp)`, Ask, unknown},
		{Rules{WriteOutsideHome: Ask}, `cd /etc && echo x > passwd`, Ask, unknown},
		{Rules{WriteOutsideHome: Deny}, `bash -c 'echo x > "$f"'`, Deny, unknown},
		{Rules{WriteOutsideHome: Ask}, `bash -c 'cd /etc; echo x > passwd'`, Ask, unknown},
		{Rules{WriteOutsideHome: Ask}, `alias w='ls > "$f"'`, Ask, unknown},
		// A known file is judged as it is, whatever runs.
		{Rules{WriteOutsideHome: Ask}, `$cmd > /etc/x`, Ask, "writes outside home: /etc/x"},
		{Rules{WriteOutsideHome: Ask}, `$cmd > ~/x`, Allow, ""},
		{Rules{WriteOutsideHome: Ask}, `"$cmd" 2>&1`, Allow, ""},
		{Rules{WriteOutsideHome: Ask}, `$cmd 2>/dev/null`, Allow, ""},
		{Rules{WriteOutsideHome: Ask}, `$cmd`, Allow, ""},
		{Rules{WriteOutsideHome: Ask}, `cd /etc && echo x > ~/x`, Allow, ""},
		// Parse does not follow where a cd works, so any cd in the line
		// makes a relative file unknown.
		{Rules{WriteOutsideHome: Ask}, `(cd /etc) && echo x > x`, Ask, unknown},
		{Rules{WriteOutsideHome: Allow}, `echo x > "$f"`, Allow, ""},
		{Rules{}, `echo x > "$f"`, Allow, ""},
		// With patterns the line asks as built at run time; the write
		// outside home may forbid it.
		{Rules{Deny: []string{"sudo *"}, WriteOutsideHome: Deny}, `echo x > "$f"`, Deny, unknown},
		{Rules{Deny: []string{"sudo *"}, WriteOutsideHome: Allow}, `echo x > "$f"`, Ask, "command built at run time (computed)"},
	} {
		e, err := Load(ctx, t.TempDir(), c.rules)
		if err != nil {
			t.Fatal(err)
		}
		d, err := e.Check(ctx, callInput("bash", map[string]any{"command": c.cmd}, home))
		if err != nil {
			t.Fatal(err)
		}
		if d.Action != c.want || d.Reason != c.reason {
			t.Errorf("%+v %s: %s (%s), want %s (%s)", c.rules, c.cmd, d.Action, d.Reason, c.want, c.reason)
		}
	}
}
