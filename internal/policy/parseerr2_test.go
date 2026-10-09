package policy

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// unparsedReason is the reason the rules ask with about a line that does
// not parse.
func unparsedReason(t *testing.T, line string) string {
	t.Helper()
	_, err := Parse(line, "", "")
	if err == nil {
		t.Fatalf("%q parses", line)
	}
	return "cannot parse the line (" + err.Error() + ")"
}

// bash runs a line command by command and stops at a syntax error: the
// lines before it have run by then, in eval and bash -c as on stdin. Parse
// keeps their commands along with the error; the commands of the line the
// error is in, a construct it breaks among them, bash does not run.
func TestParseErrorPrefix(t *testing.T) {
	sudoLs, ls := []string{"sudo", "ls"}, []string{"ls"}
	for _, c := range []struct {
		src  string
		want [][]string
	}{
		{"echo hi\nsudo ls\necho 'oops", [][]string{{"echo", "hi"}, sudoLs, ls}},
		{"sudo ls\n\n\n'oops", [][]string{sudoLs, ls}},
		{"sudo ls\necho b )\necho c", [][]string{sudoLs, ls}},
		{"sudo ls\nfi", [][]string{sudoLs, ls}},
		{"if true; then\nsudo ls\nfi\necho 'oops", [][]string{{"true"}, sudoLs, ls}},
		{"{ echo a\nsudo ls; }\necho 'oops", [][]string{{"echo", "a"}, sudoLs, ls}},
		{"sudo ls <<EOF\nx\nEOF\necho 'oops", [][]string{sudoLs, ls}},
		{"bash -c 'sudo ls\necho \"oops'", [][]string{{"bash", "-c", "sudo ls\necho \"oops"}, sudoLs, ls}},
		{"eval 'sudo ls\necho \"oops'", [][]string{{"eval", "sudo ls\necho \"oops"}, sudoLs, ls}},
		// Commands before the error on its line bash does not run; seeing
		// them is only stricter.
		{"sudo ls; echo 'oops", [][]string{sudoLs, ls}},
		// What the error breaks is not run.
		{"echo a\nif true; then\nsudo ls\necho 'oops\nfi", [][]string{{"echo", "a"}}},
		{"echo a &&\nsudo ls 'oops", nil},
		{"sudo ls 'oops", nil},
	} {
		s, err := Parse(c.src, "", "")
		if err == nil {
			t.Errorf("%q: no parse error", c.src)
		}
		if !reflect.DeepEqual(s.Commands, c.want) {
			t.Errorf("%q: commands %q, want %q", c.src, s.Commands, c.want)
		}
	}
}

// A piece of code that does not parse leaves the others in the line to be
// parsed: bash -c "'" fails, and bash runs the bash -c after it. The error
// is that of the line, else of the first piece that fails.
func TestParseErrorNested(t *testing.T) {
	sudoLs := []string{"sudo", "ls"}
	for _, c := range []struct {
		src  string
		sudo bool
		errs string
	}{
		{`bash -c "'"; bash -c 'sudo ls'`, true, "`'`"},
		{`bash -c "'"; bash -c 'echo "'; bash -c 'sudo ls'`, true, "`'`"},
		{`eval "'"; ssh box 'sudo ls'`, true, "`'`"},
		{`alias x="'" y='sudo ls'`, true, "`'`"},
		{`bash -c 'bash -c "\""; sudo ls'`, true, "`\"`"},
		{"bash -c 'echo \"'\necho 'oops", false, "2:6"},
	} {
		s, err := Parse(c.src, "", "")
		if err == nil || !strings.Contains(err.Error(), c.errs) {
			t.Errorf("%s: error %v, want one with %s", c.src, err, c.errs)
		}
		if c.sudo && !slices.ContainsFunc(s.Commands, func(a []string) bool { return slices.Equal(a, sudoLs) }) {
			t.Errorf("%s: %q not among the commands %q", c.src, sudoLs, s.Commands)
		}
	}
	// Code too deep to parse in a line of its own that does not parse.
	s, err := Parse(nest("bash -c x; echo 'oops", maxDepth), "", "")
	if err == nil || !slices.Contains(s.Dynamic, dynDepth) {
		t.Errorf("deep: error %v, dynamic %q", err, s.Dynamic)
	}
}

// A here-document left open bash ends at the end of the input, with a
// warning, and runs the command; Parse does too, and keeps the error.
func TestParseUnclosedHeredoc(t *testing.T) {
	for _, src := range []string{
		"bash <<EOF\nsudo ls",
		"bash <<EOF\nsudo ls\n",
		"bash <<'EOF'\nsudo ls",
		"bash <<\"E\"OF\nsudo ls",
		"bash <<-EOF\n\tsudo ls",
		"bash <<'E`F'\nsudo ls",
		"bash <<A <<B\nsudo ls",
		"cat <<A; bash <<B\nx\nA\nsudo ls",
		"cat <<A <<B\nx\nA\ny\nB\nbash <<C\nsudo ls",
		"echo hi\nbash -c 'bash <<EOF\nsudo ls'",
	} {
		s, err := Parse(src, "", "")
		if err == nil {
			t.Errorf("%q: no parse error", src)
		}
		if !slices.ContainsFunc(s.Commands, func(a []string) bool { return slices.Equal(a, []string{"sudo", "ls"}) }) {
			t.Errorf("%q: sudo ls not among the commands %q", src, s.Commands)
		}
	}
}

// The rules see the commands bash runs before a parse error, in the line
// and in the code it hands to a shell. Past the error the parser does not
// look, and bash may: with patterns such a line asks. Without patterns,
// with write_outside_home alone, it passes as before, and so does code
// built at run time.
func TestRulesParseError(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	const sudo = `matches "sudo *"`
	deny, err := Load(ctx, t.TempDir(), Rules{Deny: []string{"sudo *"}})
	if err != nil {
		t.Fatal(err)
	}
	writes, err := Load(ctx, t.TempDir(), Rules{WriteOutsideHome: Deny})
	if err != nil {
		t.Fatal(err)
	}
	none, err := Load(ctx, t.TempDir(), Rules{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name         string
		e            *Engine
		cmd          string
		want, reason string
	}{
		{"deny", deny, "echo hi\nsudo ls\necho 'oops", Deny, sudo},
		{"deny", deny, `bash -c "'"; bash -c 'sudo ls'`, Deny, sudo},
		{"deny", deny, "bash -c 'echo hi\nsudo ls\necho \"oops'", Deny, sudo},
		{"deny", deny, "bash <<EOF\nsudo ls", Deny, sudo},
		{"deny", deny, "echo 'oops", Ask, unparsedReason(t, "echo 'oops")},
		{"deny", deny, "echo hi\necho 'oops", Ask, unparsedReason(t, "echo hi\necho 'oops")},
		{"deny", deny, `bash -c "'"; ls`, Ask, unparsedReason(t, `bash -c "'"; ls`)},
		{"deny", deny, "echo hi", Allow, ""},
		{"write_outside_home", writes, "echo 'oops", Allow, ""},
		{"write_outside_home", writes, "echo hi\nsudo ls\necho 'oops", Allow, ""},
		{"write_outside_home", writes, `bash -c "$s"`, Allow, ""},
		{"write_outside_home", writes, `eval "$x"`, Allow, ""},
		{"write_outside_home", writes, "echo x > /etc/x\necho 'oops", Deny, "writes outside home: " + resolve("/etc/x") + "; cannot parse the line to see where it writes"},
		{"no rules", none, "echo hi\nsudo ls\necho 'oops", Allow, ""},
	} {
		d := check(t, c.e, callInput("bash", map[string]any{"command": c.cmd}, home))
		if d.Action != c.want || d.Reason != c.reason {
			t.Errorf("%s: %q: %s (%s), want %s (%s)", c.name, c.cmd, d.Action, d.Reason, c.want, c.reason)
		}
	}
}

// Cedar sees the commands before a parse error, each with parse_error in
// its context, and the request without a command as before.
func TestCedarParseError(t *testing.T) {
	ctx := context.Background()
	example, err := Load(ctx, filepath.Join("testdata", "default"), Rules{})
	if err != nil {
		t.Fatal(err)
	}
	strict := mustLoad(t, map[string]string{"a.cedar": permitAll +
		`@reason("sudo in a line that did not parse") forbid(principal, action == Action::"run", resource == Command::"sudo") when { context has parse_error };` + "\n"})
	const (
		sudo     = "sudo is not allowed for the agent"
		unparsed = "could not parse the command"
	)
	for _, c := range []struct {
		name         string
		e            *Engine
		cmd          string
		want, reason string
	}{
		{"example", example, "echo hi\nsudo ls\necho 'oops", Deny, sudo},
		{"example", example, `bash -c "'"; bash -c 'sudo ls'`, Deny, sudo},
		{"example", example, "echo hi\necho 'oops", Ask, unparsed},
		{"example", example, "echo 'oops", Ask, unparsed},
		{"strict", strict, "sudo ls\necho 'oops", Deny, "sudo in a line that did not parse"},
		{"strict", strict, "sudo ls", Allow, ""},
		{"strict", strict, "echo 'oops", Allow, ""},
	} {
		d := check(t, c.e, bash(c.cmd))
		if d.Action != c.want || d.Reason != c.reason {
			t.Errorf("%s: %q: %s (%s), want %s (%s)", c.name, c.cmd, d.Action, d.Reason, c.want, c.reason)
		}
	}
	in := bash("echo hi\nsudo ls\necho 'oops")
	if in.ParseError == "" || !reflect.DeepEqual(in.Commands, [][]string{{"echo", "hi"}, {"sudo", "ls"}, {"ls"}}) {
		t.Errorf("input: commands %q, parse error %q", in.Commands, in.ParseError)
	}
}

// The guard sees the commands before a parse error too, not only the
// names in the line's text.
func TestGuardParseError(t *testing.T) {
	ctx := context.Background()
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	if err := os.MkdirAll(filepath.Join(home, ".local", "share", "aish"), 0o700); err != nil {
		t.Fatal(err)
	}
	e, err := Load(ctx, t.TempDir(), Rules{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ cmd, want string }{
		{"cp -r x ~/.local/share/\necho 'oops", Deny},
		{"bash -c \"'\"; bash -c 'cp -r x ~/.local/share/'", Deny},
		{"echo hi\naish trust .\necho 'oops", Deny},
		{"echo 'oops trusted.json", Deny},
		{"cp -r x ~/\necho 'oops", Allow},
	} {
		d := check(t, e, callInput("bash", map[string]any{"command": c.cmd}, home))
		if d.Action != c.want {
			t.Errorf("%q: %s (%s), want %s", c.cmd, d.Action, d.Reason, c.want)
		}
	}
}
