package policy

import (
	"context"
	"slices"
	"testing"
)

// TestParseSubValue checks the code a subscript in the value of an
// assignment runs when it is read as arithmetic and the subscript is
// expanded once more: the static value the line gives a variable the
// subscript reads is put back and checked, a variable the line does not
// set makes the value computed when it is read, and a wrapper's NAME=VALUE
// of parts quoted differently is decoded part by part. has lists argv that
// must be among the commands, not those that must not; dynamic is the
// whole list of marks.
func TestParseSubValue(t *testing.T) {
	sudoLs := []string{"sudo", "ls"}
	for _, c := range []struct {
		src     string
		has     [][]string
		not     [][]string
		dynamic []string
	}{
		// The value the line gives a variable is put back into the
		// subscript that reads it.
		{`y='$(sudo ls)'; x="a[$y]"; ((x))`, [][]string{sudoLs}, nil, nil},
		{`y='$(sudo ls)'; x="a[${y}]"; ((x))`, [][]string{sudoLs}, nil, nil},
		{`y='$(sudo ls)'; x=a[$y]; ((x))`, [][]string{sudoLs}, nil, nil},
		{`declare y='$(sudo ls)'; x="a[$y]"; ((x))`, [][]string{sudoLs}, nil, nil},
		{`y='$(sudo ls)'; env "x=a[$y]" bash -c '((x))'`, [][]string{sudoLs}, nil, nil},
		{`y='$(sudo ls)'; sudo "x=a[$y]" id`, [][]string{sudoLs}, nil, nil},
		// A subscript that reads a variable not of the line, read as
		// arithmetic, is computed; one of a value the line sets to a plain
		// one, or never read as arithmetic, is not.
		{`x="a[$i]"; ((x))`, nil, nil, []string{"computed"}},
		{`x="a[$i]"; let x`, nil, nil, []string{"computed"}},
		{`x="a[$i]"; echo $((x + 1))`, nil, nil, []string{"computed"}},
		{`x="a[$i]"`, nil, nil, nil},
		{`i=1; x="a[$i]"; ((x))`, nil, nil, nil},
		{`i=1; x="a[$i]"; i='$(sudo ls)'; ((x))`, nil, [][]string{sudoLs}, nil},
		{`x="a[${y:-$(sudo ls)}]"; ((x))`, [][]string{sudoLs}, nil, []string{"computed"}},
		// Mixed quoting of a wrapper's NAME=VALUE: a $( split between a
		// \x24 that is $ and a \x5c that stays as written.
		{`env 'x=a[\x5c'$'\x24''(sudo ls)]' bash -c '((x))'`, [][]string{sudoLs}, nil, nil},
		{`strace -E 'x=a[\x5c'$'\x24''(sudo ls)]' bash -c '((x))'`, [][]string{sudoLs}, nil, nil},
		// Nothing to run.
		{`i=1; x="a[$i]"`, nil, [][]string{sudoLs}, nil},
		{`env FOO=bar ls`, nil, [][]string{sudoLs}, nil},
		{`x="a[$i]"; y=$((i+1)) z="${a[$i]}"; : ${w:=default}; for f in *.txt; do :; done`, nil, nil, nil},
	} {
		s, err := Parse(c.src, "/", "/home/me")
		if err != nil {
			t.Errorf("%s: %v", c.src, err)
			continue
		}
		in := func(argv []string) bool {
			return slices.ContainsFunc(s.Commands, func(a []string) bool { return slices.Equal(a, argv) })
		}
		for _, argv := range c.has {
			if !in(argv) {
				t.Errorf("%s: %q not among the commands %q", c.src, argv, s.Commands)
			}
		}
		for _, argv := range c.not {
			if in(argv) {
				t.Errorf("%s: %q among the commands %q", c.src, argv, s.Commands)
			}
		}
		if !slices.Equal(s.Dynamic, c.dynamic) {
			t.Errorf("%s: dynamic %q, want %q", c.src, s.Dynamic, c.dynamic)
		}
	}
}

// With deny = ["sudo *"] the code a subscript brings from a value is
// denied, a value read as arithmetic with a subscript the line does not
// resolve is asked about, and the lines without such code pass as before.
func TestSubValueRules(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	e, err := Load(ctx, t.TempDir(), Rules{Deny: []string{"sudo *"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ cmd, want string }{
		{`y='$(sudo ls)'; x="a[$y]"; ((x))`, Deny},
		{`y='$(sudo ls)'; env "x=a[$y]" bash -c '((x))'`, Deny},
		{`env 'x=a[\x5c'$'\x24''(sudo ls)]' bash -c '((x))'`, Deny},
		{`strace -E 'x=a[\x5c'$'\x24''(sudo ls)]' bash -c '((x))'`, Deny},
		{`x="a[$i]"; ((x))`, Ask},
		{`i=1; x="a[$i]"; ((x))`, Allow},
		{`x="a[$i]"`, Allow},
		{`env FOO=bar ls`, Allow},
	} {
		d, err := e.Check(ctx, callInput("bash", map[string]any{"command": c.cmd}, home))
		if err != nil {
			t.Fatal(err)
		}
		if d.Action != c.want {
			t.Errorf("%s: %s (%s), want %s", c.cmd, d.Action, d.Reason, c.want)
		}
	}
}
