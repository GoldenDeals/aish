package policy

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
)

// hasSudoLs tells whether sudo ls is among the commands.
func hasSudoLs(cmds [][]string) bool {
	return slices.ContainsFunc(cmds, func(a []string) bool { return slices.Equal(a, []string{"sudo", "ls"}) })
}

// An error in arithmetic, or in a ${…} bash cannot expand, the parser
// stops at, and bash makes only when it runs the command: the lines after
// it run, and so do the substitutions inside it. Parse rewrites the
// construct to get past it, sees the commands after it and in it, marks
// the line computed and keeps the error.
func TestParseArithError(t *testing.T) {
	for _, src := range []string{
		"((1 +))\nsudo ls",
		"x=$((1 +)); sudo ls",
		"echo $[1 +]\nsudo ls",
		"for ((i=0;i<;i++)); do :; done\nsudo ls",
		"for((i=0;i<;i++)) do :; done\nsudo ls",
		"echo $(( $(sudo ls) + ))",
		"echo $(( `sudo ls` + ))",
		"echo \"$(( $(sudo ls) + ))\"",
		// Arithmetic is expanded as in double quotes: bash runs what is in
		// single quotes in it.
		"echo $(( '$(sudo ls)' + ))",
		"echo $(( \"$(sudo ls)\" + ))",
		"echo $(( ${x:-$(sudo ls)} + ))",
		"echo $(( $(echo \")\") + ))\nsudo ls",
		"echo $(( ))\nsudo ls",
		"(( ))\nsudo ls",
		"echo $(( 1 2 ))\nsudo ls",
		"echo $(( (1 + ) ))\nsudo ls",
		"echo $(( ((1 +)) ))\nsudo ls",
		"echo $(( 1 +\n))\nsudo ls",
		"echo $((1 +)) $((2 +))\nsudo ls",
		"case $((1 +)) in *) ;; esac\nsudo ls",
		"echo hi > $((1 +))\nsudo ls",
		"cat <<EOF\n$((1 +))\nEOF\nsudo ls",
		// An index, and a ${…}, bash checks only when it expands them.
		"a[1 +]=3\nsudo ls",
		"a[$(sudo ls) +]=3",
		"declare a[1 +]=3\nsudo ls",
		"echo ${a[1 +]}\nsudo ls",
		"echo $(( x[1 +] ))\nsudo ls",
		"echo ${x:1 +}\nsudo ls",
		"echo \"${x:$(sudo ls) +}\"",
		"echo ${x:1:2 +}\nsudo ls",
		"echo ${x@Z}; sudo ls",
		"echo \"${x@Z}\"; sudo ls",
		// With a here-document left open, and in the code of a line.
		"((1 +))\nbash <<EOF\nsudo ls",
		"bash <<EOF; ((1 +))\nsudo ls",
		"eval '((1 +))\nsudo ls'",
		"bash -c 'echo $((1 +))\nsudo ls'",
	} {
		s, err := Parse(src, "", "")
		if err == nil {
			t.Errorf("%q: no parse error", src)
			continue
		}
		if !hasSudoLs(s.Commands) {
			t.Errorf("%q: sudo ls not among the commands %q", src, s.Commands)
		}
		if !slices.Contains(s.Dynamic, dynComputed) {
			t.Errorf("%q: dynamic %q, want computed", src, s.Dynamic)
		}
	}
}

// The error is the one the parser gave, of the line or of its code.
func TestParseArithErrorKept(t *testing.T) {
	for _, c := range []struct{ src, want string }{
		{"((1 +))\nsudo ls", "1:5: `+` must be followed by an expression"},
		{"eval 'echo $[1 +]'; sudo ls", "1:10: `+` must be followed by an expression"},
		{"echo ${x@Z} $((1 +))\nsudo ls", "1:10: invalid @ expansion operator `Z`"},
	} {
		if _, err := Parse(c.src, "", ""); err == nil || err.Error() != c.want {
			t.Errorf("%q: error %v, want %s", c.src, err, c.want)
		}
	}
}

// A line without an error parses as before; one whose error is not in
// arithmetic, or whose arithmetic does not end, keeps only the commands
// before it, unmarked.
func TestParseArithUnchanged(t *testing.T) {
	for _, c := range []struct {
		src  string
		fail bool
	}{
		{"echo $((1 + 2))", false},
		{"((i++)); a[i]=1; echo ${x:1:2} ${a[1]}", false},
		{"echo $((1 +", true},
		{"echo $(( $(if) + ))\nsudo ls", true},
		{"echo $(( 1 +\nsudo ls", true},
		{"echo $(( \"1 + ))\nsudo ls", true},
		{"echo 'oops\nsudo ls", true},
	} {
		s, err := Parse(c.src, "", "")
		if (err != nil) != c.fail {
			t.Errorf("%q: error %v", c.src, err)
		}
		if len(s.Dynamic) > 0 || hasSudoLs(s.Commands) {
			t.Errorf("%q: commands %q, dynamic %q", c.src, s.Commands, s.Dynamic)
		}
	}
}

// deny = ["sudo *"] denies sudo after an error in arithmetic and inside
// it, and Cedar does too.
func TestPolicyArithError(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	deny, err := Load(ctx, t.TempDir(), Rules{Deny: []string{"sudo *"}})
	if err != nil {
		t.Fatal(err)
	}
	example, err := Load(ctx, filepath.Join("..", "..", "examples", "policy"), Rules{})
	if err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{
		"((1 +))\nsudo ls",
		"x=$((1 +)); sudo ls",
		"echo $[1 +]\nsudo ls",
		"for ((i=0;i<;i++)); do :; done\nsudo ls",
		"echo $(( $(sudo ls) + ))",
	} {
		in := callInput("bash", map[string]any{"command": cmd}, home)
		if d := check(t, deny, in); d.Action != Deny || d.Reason != `matches "sudo *"` {
			t.Errorf("rules: %q: %s (%s), want deny", cmd, d.Action, d.Reason)
		}
		if d := check(t, example, in); d.Action != Deny || d.Reason != "sudo is not allowed for the agent" {
			t.Errorf("example: %q: %s (%s), want deny", cmd, d.Action, d.Reason)
		}
	}
	if d := check(t, deny, callInput("bash", map[string]any{"command": "echo $((1 + 2))"}, home)); d.Action != Allow {
		t.Errorf("rules: echo $((1 + 2)): %s (%s), want allow", d.Action, d.Reason)
	}
}
