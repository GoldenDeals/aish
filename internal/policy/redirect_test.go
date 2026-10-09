package policy

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestParseWrites checks the files the redirections of a line write: in
// any statement and in the code the line hands to a shell, with ~, $HOME
// and $PWD expanded and a relative path taken from cwd; descriptors and
// devices are no files, and a file not known before the line runs is
// marked.
func TestParseWrites(t *testing.T) {
	home, out := links(t, map[string]string{
		"link": "/etc",
		"deep": "$out/deep/er",
	})
	for _, c := range []struct {
		src     string
		writes  []string
		dynamic []string
	}{
		{`echo x > /etc/x`, []string{"/etc/x"}, nil},
		{`echo x >> ~/.bashrc`, []string{home + "/.bashrc"}, nil},
		{`{ ls; } > out`, []string{"/w/out"}, nil},
		{`while true; do ls; done >> out`, []string{"/w/out"}, nil},
		{`f() { ls; } > /etc/x`, []string{"/etc/x"}, []string{"prompt"}},
		{`cat <> f`, []string{"/w/f"}, nil},
		{`ls &> log`, []string{"/w/log"}, nil},
		{`ls &>> log`, []string{"/w/log"}, nil},
		{`ls >| log`, []string{"/w/log"}, nil},
		{`ls >& log`, []string{"/w/log"}, nil},
		{`ls 1>&log`, []string{"/w/log"}, nil},
		{`ls > a 2> b; ls >> a`, []string{"/w/a", "/w/b"}, nil},
		{`ls > ./x/../y`, []string{"/w/y"}, nil},

		{`ls 2>/dev/null`, nil, nil},
		{`ls >/dev/null 2>&1`, nil, nil},
		{`ls >&2`, nil, nil},
		{`ls 2>&1`, nil, nil},
		{`exec 3>&-`, nil, nil},
		{`exec 4>&3-`, nil, nil},
		{`ls > /dev/stdout 2> /dev/stderr > /dev/tty > /dev/fd/3`, nil, nil},
		{`cat < /etc/passwd; cat <<< x; cat <&3`, nil, nil},

		{`ls > $HOME/x`, []string{home + "/x"}, nil},
		{`ls > "${HOME}/x"`, []string{home + "/x"}, nil},
		{`ls > "$HOME"/x`, []string{home + "/x"}, nil},
		{`ls > $PWD/x`, []string{"/w/x"}, nil},
		// Quoted, the tilde is a name in cwd.
		{`ls > "~/x"`, []string{"/w/~/x"}, nil},
		{`ls > \~/x`, []string{"/w/~/x"}, nil},

		// Through a symlink, and up from where it really leads.
		{`echo x > ~/link/x`, []string{"/etc/x"}, nil},
		{`echo x > ~/deep/../y`, []string{out + "/deep/y"}, nil},

		{`echo > "$f"`, nil, []string{"computed"}},
		{`echo > $(mktemp)`, nil, []string{"computed"}},
		{`echo > *.log`, nil, []string{"computed"}},
		{`echo > ~/lin?/x`, nil, []string{"computed"}},
		{`echo > ~root/.bashrc`, nil, []string{"computed"}},
		{`echo > ~+/x`, nil, []string{"computed"}},
		{`echo > $HOME/$f`, nil, []string{"computed"}},
		{`ls >&$fd`, nil, []string{"computed"}},
		{`cd /etc && echo > passwd`, nil, []string{"computed"}},
		{`builtin cd /etc; echo > $PWD/passwd`, nil, []string{"computed"}},
		{`pushd /etc; echo > passwd`, nil, []string{"computed"}},
		{`(cd /etc) && echo > /tmp/x`, []string{"/tmp/x"}, nil},
		{`cd /etc && echo > ~/x`, []string{home + "/x"}, nil},

		{`bash -c 'echo x > /etc/x'`, []string{"/etc/x"}, nil},
		{`eval 'echo x > /etc/x'`, []string{"/etc/x"}, nil},
		{`echo $(echo x > /etc/x)`, []string{"/etc/x"}, nil},
		{`ls | tee >(cat > /etc/x)`, []string{"/etc/x"}, nil},
		{"bash <<'EOF'\necho x > /etc/x\nEOF", []string{"/etc/x"}, nil},
		{`alias x='ls > /etc/x'`, []string{"/etc/x"}, []string{"prompt"}},
		{`bash -c 'cd /etc; echo x > passwd'`, nil, []string{"computed"}},
	} {
		s, err := Parse(c.src, "/w", home)
		if err != nil {
			t.Errorf("%s: %v", c.src, err)
			continue
		}
		if !slices.Equal(s.Writes, c.writes) {
			t.Errorf("%s: writes %q, want %q", c.src, s.Writes, c.writes)
		}
		if !slices.Equal(s.Dynamic, c.dynamic) {
			t.Errorf("%s: dynamic %q, want %q", c.src, s.Dynamic, c.dynamic)
		}
	}
	if s, _ := Parse(`echo x > deep/../y`, home, home); !slices.Equal(s.Writes, []string{out + "/deep/y"}) {
		t.Errorf("deep/../y from home: writes %q", s.Writes)
	}
	// Without a cwd, as for journal_ignore, nothing is resolved.
	if s, _ := Parse(`echo x > /etc/x`, "", ""); s.Writes != nil {
		t.Errorf("writes %q without a cwd", s.Writes)
	}
}

// Cedar gets a write of File for each file, with the tool and the
// directories of write_file; the arguments of the call are no tags of it.
func TestRedirectCedar(t *testing.T) {
	home, _ := links(t, nil)
	t.Setenv("HOME", home)
	e := mustLoad(t, map[string]string{"a.cedar": permitAll + `@reason("out of home by ssh")
forbid(principal, action == Action::"write", resource)
when { context.tool == "ssh" && !(resource in Dir::"~") };
@reason("tagged")
forbid(principal, action == Action::"write", resource)
when { resource.hasTag("command") };
@ask("overwrites")
forbid(principal, action == Action::"write", resource)
when { context.exists && context.path like "*/old" };
`})
	if err := os.WriteFile(filepath.Join(home, "old"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ssh := func(line string) Input {
		in := Input{Tool: "ssh", Cwd: home, Home: home}
		in.HandOff(line)
		return in
	}
	for _, c := range []struct {
		in   Input
		want string
	}{
		{ssh("echo x > /etc/x"), Deny},
		{ssh("echo x > ~/x"), Allow},
		{bash("echo x > /etc/x"), Allow},
		{ssh("echo x > old"), Ask},
		{ssh("echo x > new"), Allow},
	} {
		if d := check(t, e, c.in); d.Action != c.want {
			t.Errorf("%s %s: %+v, want %s", c.in.Tool, c.in.Line, d, c.want)
		}
	}
}

// The example's rule on writing outside $HOME holds for a redirection of a
// bash command as for write_file.
func TestRedirectExample(t *testing.T) {
	ctx := context.Background()
	home, _ := links(t, map[string]string{"link": "/etc"})
	t.Setenv("HOME", home)
	e, err := Load(ctx, filepath.Join("..", "..", "examples", "policy"), Rules{})
	if err != nil {
		t.Fatal(err)
	}
	const outside = "writing outside $HOME"
	for _, c := range []struct{ cmd, want, reason string }{
		{"echo x > /etc/x", Deny, outside},
		{"echo x >> /etc/x", Deny, outside},
		{"ls &> /etc/x", Deny, outside},
		{"echo x > ~/link/x", Deny, outside},
		{"bash -c 'echo x > /etc/x'", Deny, outside},
		{"echo x > /tmp/x", Allow, ""},
		{"echo x > ~/x", Allow, ""},
		{"echo x > x", Allow, ""},
		{"ls 2>/dev/null", Allow, ""},
		{"ls > /dev/null 2>&1", Allow, ""},
		{"cat < /etc/passwd", Allow, ""},
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

// write_outside_home decides for the redirections of a bash command as for
// write_file.
func TestRedirectRules(t *testing.T) {
	ctx := context.Background()
	home, _ := links(t, map[string]string{"link": "/etc"})
	t.Setenv("HOME", home)
	for _, c := range []struct{ setting, cmd, want, reason string }{
		{Ask, "echo x > /etc/x", Ask, "writes outside home: /etc/x"},
		{Ask, "echo x > ~/x", Allow, ""},
		{Ask, "echo x > ~/link/x", Ask, "writes outside home: /etc/x"},
		{Ask, "ls 2>/dev/null", Allow, ""},
		{Deny, "make; echo x >> /etc/x", Deny, "writes outside home: /etc/x"},
		{Deny, "echo x > x", Allow, ""},
		{"", "echo x > /etc/x", Allow, ""},
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
			t.Errorf("write_outside_home = %q, %s: %s (%s), want %s (%s)", c.setting, c.cmd, d.Action, d.Reason, c.want, c.reason)
		}
	}
}
