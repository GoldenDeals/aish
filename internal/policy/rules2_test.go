package policy

import (
	"context"
	"testing"
)

// A pattern names a program by its name: /usr/bin/sudo and ./sudo run
// sudo as much as sudo does. What the shell takes away from a word (a
// backslash, quotes) Parse takes away too, and wrappers are looked behind.
func TestRulesProgramName(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	e, err := Load(ctx, t.TempDir(), Rules{
		Deny: []string{"sudo *", "rm -rf /"},
		Ask:  []string{"apt install *"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ cmd, want, reason string }{
		{"/usr/bin/sudo ls", Deny, `matches "sudo *"`},
		{"./sudo x", Deny, `matches "sudo *"`},
		{"/usr/bin/sudo", Deny, `matches "sudo *"`},
		{`"/usr/bin/sudo" ls`, Deny, `matches "sudo *"`},
		{`\sudo ls`, Deny, `matches "sudo *"`},
		{`command sudo ls`, Deny, `matches "sudo *"`},
		{`\command /usr/bin/sudo ls`, Deny, `matches "sudo *"`},
		{`env /usr/bin/sudo ls`, Deny, `matches "sudo *"`},
		{`/usr/bin/env -S '/usr/bin/sudo ls'`, Deny, `matches "sudo *"`},
		{`echo x | /usr/bin/sudo tee /etc/x`, Deny, `matches "sudo *"`},
		{`ssh box /usr/bin/sudo ls`, Deny, `matches "sudo *"`},
		{"/bin/rm -rf /", Deny, `matches "rm -rf /"`},
		{"/usr/bin/apt install x", Ask, `matches "apt install *"`},
		// A line that does not parse is matched whole, by its first word's
		// name too.
		{"/usr/bin/sudo ls 'oops", Deny, `matches "sudo *"`},
		{`\sudo ls 'oops`, Deny, `matches "sudo *"`},
		{`"/usr/bin/sudo" ls 'oops`, Deny, `matches "sudo *"`},
		{"/usr/bin/sudo ls\necho 'oops", Deny, `matches "sudo *"`},
		{"  sudo ls\necho 'oops", Deny, `matches "sudo *"`},
		// The name is the program's, not an operand's; a line that does not
		// parse asks all the same.
		{"ls /usr/bin/sudo", Allow, ""},
		{"/usr/bin/sudoedit x", Allow, ""},
		{"/opt/xsudo ls", Allow, ""},
		{"/bin/rm -rf /tmp/x", Allow, ""},
		{"echo /usr/bin/sudo ls 'oops", Ask, unparsedReason(t, "echo /usr/bin/sudo ls 'oops")},
	} {
		d, err := e.Check(ctx, callInput("bash", map[string]any{"command": c.cmd}, home))
		if err != nil {
			t.Fatal(err)
		}
		if d.Action != c.want || d.Reason != c.reason {
			t.Errorf("%q: %s (%s), want %s (%s)", c.cmd, d.Action, d.Reason, c.want, c.reason)
		}
	}
}

// A line that does not parse may write anywhere: bash runs its lines up to
// the error, and the code it hands to a shell is in it too, in quotes. With
// a > anywhere in it write_outside_home takes it for a write outside home.
func TestRulesUnparsedWrite(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	const unparsed = "cannot parse the line to see where it writes"
	etc := "writes outside home: " + resolve("/etc/x")
	for _, c := range []struct {
		setting      string
		cmd          string
		want, reason string
	}{
		{Deny, `echo x > /etc/x 'oops`, Deny, unparsed},
		{Ask, `echo x > /etc/x 'oops`, Ask, unparsed},
		{Allow, `echo x > /etc/x 'oops`, Allow, ""},
		{"", `echo x > /etc/x 'oops`, Allow, ""},
		// The lines before the error are parsed, and their writes known.
		{Deny, "echo x >> /etc/x\necho 'oops", Deny, etc + "; " + unparsed},
		{Deny, `echo x &>/etc/x "oops`, Deny, unparsed},
		{Deny, "bash -c 'echo x > /etc/x\necho \"oops'", Deny, etc + "; " + unparsed},
		{Deny, `echo 'oops`, Allow, ""},
		{Deny, `cat < /etc/x 'oops`, Allow, ""},
	} {
		e, err := Load(ctx, t.TempDir(), Rules{WriteOutsideHome: c.setting})
		if err != nil {
			t.Fatal(err)
		}
		d, err := e.Check(ctx, callInput("bash", map[string]any{"command": c.cmd}, home))
		if err != nil {
			t.Fatal(err)
		}
		if d.Action != c.want || d.Reason != c.reason {
			t.Errorf("write_outside_home = %q %q: %s (%s), want %s (%s)", c.setting, c.cmd, d.Action, d.Reason, c.want, c.reason)
		}
	}
	// A redirection to a file known only at run time in code after the
	// one that does not parse is no less a write.
	e, err := Load(ctx, t.TempDir(), Rules{WriteOutsideHome: Deny})
	if err != nil {
		t.Fatal(err)
	}
	cmd := `bash -c "'"; bash -c 'echo x > "$f"'`
	if d := check(t, e, callInput("bash", map[string]any{"command": cmd}, home)); d.Action != Deny {
		t.Errorf("%s: %+v, want deny", cmd, d)
	}
}

// Parse tells a redirection to a file known only at run time from a
// program built at run time: both are computed, only the first writes a
// file no policy knows. The command of ssh writes on another machine.
func TestScriptUnknownWrite(t *testing.T) {
	home := t.TempDir()
	for _, c := range []struct {
		line string
		want bool
	}{
		{`echo x > "$f"`, true},
		{`echo x >> $(mktemp)`, true},
		{`echo x > ~user/x`, true},
		{`cd /etc && echo x > passwd`, true},
		{`(cd /etc) && echo x > x`, true},
		{`bash -c 'echo x > "$f"'`, true},
		{`su -c 'echo x > "$f"'`, true},
		{`alias w='ls > "$f"'`, true},
		{`{ ls; } > "$f"`, true},
		{`$cmd > /etc/x`, false},
		{`$cmd > ~/x`, false},
		{`"$cmd" 2>&1`, false},
		{`$cmd 2>/dev/null`, false},
		{`$cmd`, false},
		{`echo x > /etc/x`, false},
		{`cd /etc && echo x > ~/x`, false},
		{`echo x > "$HOME/x"`, false},
		{`ssh box 'echo x > "$f"'`, false},
		{`ssh box 'cd /etc && echo x > passwd'`, false},
		{`eval "$x"`, false},
	} {
		s, _ := Parse(c.line, home, home)
		if s.UnknownWrite != c.want {
			t.Errorf("%s: UnknownWrite %v, want %v", c.line, s.UnknownWrite, c.want)
		}
		in := Input{Cwd: home, Home: home}
		in.HandOff(c.line)
		if in.UnknownWrite != c.want {
			t.Errorf("%s: Input.UnknownWrite %v, want %v", c.line, in.UnknownWrite, c.want)
		}
	}
}
