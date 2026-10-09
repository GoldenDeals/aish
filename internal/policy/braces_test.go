package policy

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/syntax"
)

// bash expands braces before it takes the quotes away: in {'sudo',ls} the
// braces and the comma are out of quotes, and the line runs sudo ls. With
// deny = ["sudo *"] a program or a wrapper's command made so asks, as
// {sudo,ls} does; so does one a glob makes across quotes,
// /usr/bin/['s']udo, and a variable such words assign. What the words as
// written give stays: eval of the text still sees sudo. Braces whose comma
// or } is quoted, or a backslash before them, are no expansion, and a word
// of quotes alone stays what it was.
func TestRulesBraceQuotes(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	e, err := Load(ctx, t.TempDir(), Rules{Deny: []string{"sudo *"}})
	if err != nil {
		t.Fatal(err)
	}
	sudo, computed, rebind := `matches "sudo *"`, "command built at run time (computed)", "command built at run time (rebind)"
	for _, c := range []struct{ cmd, want, reason string }{
		{`{'sudo',ls}`, Ask, computed},
		{`{"sudo",ls}`, Ask, computed},
		{`s{'u',}do ls`, Ask, computed},
		{`{sudo,ls}`, Ask, computed},
		{`env {'sudo',ls}`, Ask, computed},
		{`{'su''do',ls}`, Ask, computed},
		{`{"sudo","rm"} -rf /`, Ask, computed},
		{`x{'a',b}`, Ask, computed},
		{`nice -n 5 {'sudo',ls}`, Ask, computed},
		{`command {'sudo',ls}`, Ask, computed},
		{`time {'sudo',ls}`, Ask, computed},
		{`xargs {'sudo',ls}`, Ask, computed},
		{`timeout 5 {'sudo',ls}`, Ask, computed},
		{`find . -exec {'sudo',ls} \;`, Ask, computed},
		{`echo $({'sudo',ls})`, Ask, computed},
		{`bash -c {'sudo ls',}`, Ask, computed},
		{`trap {'sudo ls',} EXIT`, Ask, "command built at run time (computed, prompt)"},
		{`su -c {'sudo ls',}`, Ask, computed},
		{`ssh host {'sudo ls',}`, Ask, computed},
		{`{'cd',} /etc && rm -rf passwd`, Ask, computed},
		// Nested braces, and a } bash does not take for the end before
		// the first comma: {X=}y,sudo} is X=}y and sudo.
		{`env {X={a},sudo} ls`, Ask, computed},
		{`env {X=}y,sudo} ls`, Ask, computed},
		{`env {X='}'y,sudo} ls`, Ask, computed},
		// A bracket expression with quoted text in it.
		{`/usr/bin/['s']udo ls`, Ask, computed},
		{`/usr/bin/sud["o"] ls`, Ask, computed},
		{`/usr/bin/sud[o'x'] ls`, Ask, computed},
		// Names and values that braces make: export {'PATH',x}=/tmp
		// assigns PATH, for and an array take each word.
		{`export {'PATH',x}=/tmp`, Ask, computed},
		{`unset {'PATH',x}`, Ask, rebind},
		{`read {'PATH',x}`, Ask, computed},
		{`for PAGER in {'sudo ls',x}; do man ls; done`, Ask, computed},
		{`PAGER=({'sudo ls',x}); man ls`, Ask, computed},

		// The text as written is looked at as before.
		{`eval 'sudo ls; x'{'a',b}`, Deny, sudo},
		{`bash -c 'sudo ls; x'{'a',b}`, Deny, sudo},
		{`eval 'sudo ls; '{a}x,y}`, Deny, sudo},
		{`PAGER='sudo ls; x'{'a',b} man ls`, Deny, sudo},
		{`x='$(sudo ls)'{'a',b}; echo "${x@P}"`, Deny, sudo},
		{`sudo ls`, Deny, sudo},

		{`echo {a,b}`, Allow, ""},
		{`echo '{a,b}'`, Allow, ""},
		{`echo "${x}"`, Allow, ""},
		{`echo \{a,b\}`, Allow, ""},
		{`echo {'a',b} a["k"]`, Allow, ""},
		{`touch {"a b",c}`, Allow, ""},
		{`a=({'x',y}); x={'a',b}`, Allow, ""},
		{`for i in {'x',y}; do echo "$i"; done`, Allow, ""},
		{`{'sudo'}`, Allow, ""},
		{`'{'sudo,ls'}'`, Allow, ""},
		{`{sudo,ls'}'`, Allow, ""},
		{`{sudo','ls}`, Allow, ""},
		{`"{sudo,ls}"`, Allow, ""},
		{`\{sudo,ls}`, Allow, ""},
		{`/usr/bin/sud'['o] ls`, Allow, ""},
		{`find . -name '*.go' -exec grep -l x {} +`, Allow, ""},
		{`git show HEAD@{1}`, Allow, ""},
		{`cp x{,.bak} /tmp`, Allow, ""},
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

// A redirection to a file braces with quotes make writes where the line
// tells only at run time: > {'/etc/x',} writes /etc/x.
func TestRulesBraceQuotesWrite(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	e, err := Load(ctx, t.TempDir(), Rules{WriteOutsideHome: Deny})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ cmd, want string }{
		{`echo x > {'/etc/x',}`, Deny},
		{`echo x >> /etc/['x']`, Deny},
		{`echo x > {/etc/x,}`, Deny},
		{`echo x > '{/etc/x,}'`, Allow},
		{`echo x > x{'a'}`, Allow},
	} {
		d, err := e.Check(ctx, callInput("bash", map[string]any{"command": c.cmd}, home))
		if err != nil {
			t.Fatal(err)
		}
		if d.Action != c.want {
			t.Errorf("%q: %s (%s), want %s", c.cmd, d.Action, d.Reason, c.want)
		}
	}
}

// quotedExpansion tells of the words bash expands that isStatic and
// splits take as written.
func TestQuotedExpansion(t *testing.T) {
	for _, c := range []struct {
		word string
		want bool
	}{
		{`{'sudo',ls}`, true},
		{`{"sudo",ls}`, true},
		{`s{'u',}do`, true},
		{`{'a',b}x`, true},
		{`a{'b'}c,d}`, true},
		{`{X=}y,sudo}`, true},
		{`{X={a},sudo}`, true},
		{`['s']udo`, true},
		{`a["k"]`, true},
		{`{'a',"$x"}`, true},
		{`{a,b}`, false},
		{`"$x"{a,b}`, false},
		{`x{"b,c"}`, false},
		{`{'sudo'}`, false},
		{`'{a,b}'`, false},
		{`\{a,b\}`, false},
		{`{a,b'}'`, false},
		{`'['s]udo`, false},
		{`sud[o']'`, false},
		{`"{"a,b"}"`, false},
		{`"${x}"`, false},
	} {
		if got := quotedExpansion(parseWord(t, c.word)); got != c.want {
			t.Errorf("%s: %v, want %v", c.word, got, c.want)
		}
	}
}

// braceExpansion finds braces as bash does, with the } it does not take
// for the end before the first comma; checked against bash 5.3.
func TestBraceExpansion(t *testing.T) {
	for _, c := range []struct {
		s          string
		found, odd bool
	}{
		{"{a,b}", true, false},
		{"x{,}", true, false},
		{"{a..c}", true, false},
		{"{a,b}}", true, false},
		{"{{,}", true, false},
		{"{a,{b,c}", true, false},
		{"{x,{a,b}}", true, false},
		{`{a\,b,c}`, true, false},
		{"{a..b}x,y}", true, false},
		{`{\}x,y}`, true, false},
		// x, {a}b and c; a:{b:1} and c:2.
		{"{x,{a}b,c}", true, false},
		{"{x:{x:1},x:2}", true, false},
		{"{p,{{a}x,y}}", true, false},
		// a}x and y.
		{"{a}x,y}", true, true},
		{"{a}}x,y}", true, true},
		{"x{a}b,c}d", true, true},
		{"{a..}x,y}", true, true},
		{"{a}{b}x,y}", true, true},
		{"{a,b}{c}x,y}", true, true},
		// No sequence: {a}b..d} and {a}c..d}, as mvdan.cc/sh has them.
		{"{a}{b,c}..d}", true, false},
		{"{a}x..y}", true, false},
		{"{a..c,d}}", true, false},

		{"{}", false, false},
		{"{a}", false, false},
		{"{a}x,y", false, false},
		{"{a}{b,c", false, false},
		{"{a..}", false, false},
		{`{a\,b}`, false, false},
		{`{a,b\}`, false, false},
		{`\{a,b}`, false, false},
		{"--format={a},{b}", false, false},
		{"{{.x}},{{.y}}", false, false},
		{"a{b}c", false, false},
		{"{,", false, false},
	} {
		found, odd := braceExpansion(c.s)
		if found != c.found || odd != c.odd {
			t.Errorf("%s: found %v odd %v, want %v %v", c.s, found, odd, c.found, c.odd)
		}
	}
}

// Braces with quoted text between them add the paths they make to those of
// the command, as without quotes; braces bash ends past a } are more than
// mvdan.cc/sh expands, and mark the line.
func TestLinePathsBraceQuotes(t *testing.T) {
	root := lineTree(t)
	work, etc, me := root+"/work", root+"/etc", root+"/home/me"
	for _, c := range []struct {
		cmd, text string
		want      []string
		computed  bool
	}{
		{"rm -rf {'" + etc + "/passwd',x}", "rm -rf {" + etc + "/passwd,x}", []string{work + "/{" + etc + "/passwd,x}", etc + "/passwd"}, false},
		{"rm -rf " + root + "/{'etc',home}/x", "rm -rf " + root + "/{etc,home}/x", []string{root + "/{etc,home}/x", etc + "/x", root + "/home/x"}, false},
		{"rm -rf " + root + `/{"et"c,home}/x`, "rm -rf " + root + "/{etc,home}/x", []string{root + "/{etc,home}/x", etc + "/x", root + "/home/x"}, false},
		{"rm -rf " + etc + "/{'pass*',x}", "rm -rf " + etc + "/{pass*,x}", []string{etc + "/{pass*,x}", etc + "/pass*", etc + "/x"}, false},
		{"rm -rf {a}x," + etc + "/passwd}", "", nil, true},
	} {
		in := lineInput(c.cmd, work, me)
		if c.want != nil {
			if got := commandPaths(t, in, c.text); !slices.Equal(got, sorted(c.want...)) {
				t.Errorf("%s: paths %q, want %q", c.cmd, got, sorted(c.want...))
			}
		}
		if got := slices.Contains(in.Dynamic, "computed"); got != c.computed {
			t.Errorf("%s: dynamic %q, want computed %v", c.cmd, in.Dynamic, c.computed)
		}
	}
}

// The guard sees the trust file in braces bash ends past a }, and in a
// redirection to braces or a glob with quotes as it did.
func TestGuardBraceQuotes(t *testing.T) {
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
		{"tee {a}x,~/.local/share/aish/trusted.json} </dev/null", Deny},
		{"tee {'x',~/.local/share/aish/trusted.json} </dev/null", Deny},
		{"echo x > ~/.local/share/a['i']sh/trusted['.']json", Deny},
		{"echo x > ~/.local/share/a{'i',x}sh/trus{'t',}ed.json", Deny},
		{"tee {x,y} </dev/null", Allow},
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

// parseWord is the first word of the command src.
func parseWord(t *testing.T, src string) *syntax.Word {
	t.Helper()
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader("echo "+src), "")
	if err != nil {
		t.Fatal(err)
	}
	return f.Stmts[0].Cmd.(*syntax.CallExpr).Args[1]
}
