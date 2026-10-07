package policy

import (
	"context"
	"slices"
	"testing"
)

// TestParseLeftovers checks the code bash runs that no command of the line
// shows: what ${x@P} expands, the subscripts in the values of NAME=VALUE
// that wrappers put in the environment, and $((…) …) and ((…) …), which
// bash takes for a substitution and a subshell. has lists argv that must
// be among the commands, not those that must not; dynamic is the whole
// list of marks, and fails tells that the line keeps its parse error.
func TestParseLeftovers(t *testing.T) {
	sudoLs := []string{"sudo", "ls"}
	for _, c := range []struct {
		src     string
		has     [][]string
		not     [][]string
		dynamic []string
		fails   bool
	}{
		// ${x@P}: computed whatever the value, its code parsed when the
		// line gives it, before the expansion or after it.
		{`y='$(sudo ls)'; echo "${y@P}"`, [][]string{sudoLs}, nil, []string{"computed"}, false},
		{`y='$(sudo ls)'; echo ${y@P}`, [][]string{sudoLs}, nil, []string{"computed"}, false},
		{"y='`sudo ls`'; echo \"${y@P}\"", [][]string{sudoLs}, nil, []string{"computed"}, false},
		{`a=(x '$(sudo ls)'); echo "${a[@]@P}"`, [][]string{sudoLs}, nil, []string{"computed"}, false},
		{`declare -A h=([k]='$(sudo ls)'); echo "${h[k]@P}"`, [][]string{sudoLs}, nil, []string{"computed"}, false},
		{`export y='$(sudo ls)'; echo "${y@P}"`, [][]string{sudoLs}, nil, []string{"computed"}, false},
		{`f() { echo "${y@P}"; }; y='$(sudo ls)'; f`, [][]string{sudoLs}, nil, []string{"computed"}, false},
		{`env y='$(sudo ls)' bash -c 'echo "${y@P}"'`, [][]string{sudoLs}, nil, []string{"computed"}, false},
		{`y='$(sudo ls)'; eval 'echo "${y@P}"'`, [][]string{sudoLs}, nil, []string{"computed"}, false},
		{`eval "y='\$(sudo ls)'"; echo "${y@P}"`, [][]string{sudoLs}, nil, []string{"computed"}, false},
		{`y='$(sudo ls)'; cat <<EOF` + "\n${y@P}\nEOF", [][]string{sudoLs}, nil, []string{"computed"}, false},
		{`y='$(sudo ls)'; (( '${y@P}' ))`, [][]string{sudoLs}, nil, []string{"computed"}, false},
		{`y='$(sudo ls)'; let 'a[${y@P}]=1'`, [][]string{sudoLs}, nil, []string{"computed"}, false},
		// The escapes of a prompt string are decoded first.
		{`y='\044(sudo ls)'; echo "${y@P}"`, [][]string{sudoLs}, nil, []string{"computed"}, false},
		{`y='\140sudo ls\140'; echo "${y@P}"`, [][]string{sudoLs}, nil, []string{"computed"}, false},
		{`y='$(echo\nsudo ls)'; echo "${y@P}"`, [][]string{{"echo"}, sudoLs}, nil, []string{"computed"}, false},
		{`y='\$(sudo ls) \44(sudo ls) \000(sudo ls)'; echo "${y@P}"`, nil, [][]string{sudoLs}, []string{"computed"}, false},
		// A value not of the line, or of a name the line does not tell.
		{`echo "${PS1@P}"`, nil, nil, []string{"computed"}, false},
		{`read y; echo "${y@P}"`, nil, nil, []string{"computed"}, false},
		{`n=y; y='$(sudo ls)'; echo "${!n@P}"`, nil, nil, []string{"computed"}, false},
		{`f() { echo "${1@P}"; }; f '$(sudo ls)'`, nil, nil, []string{"computed"}, false},
		{`y='${y@P}'; echo "${y@P}"`, nil, nil, []string{"computed"}, false},
		// The other operators run no code of the value.
		{`y='$(sudo ls)'; echo "${y@Q}" "${y@E}" "${y@A}" "${y@K}" "${y@a}" "${y@U}" "${y@u}" "${y@L}"`, nil, [][]string{sudoLs}, nil, false},
		{`echo "${x@Q}"`, nil, nil, nil, false},

		// NAME=VALUE of a wrapper: its value is read as that of x=… is.
		{`env 'x=a[$(sudo ls)]' bash -c '((x))'`, [][]string{sudoLs}, nil, nil, false},
		{`sudo 'x=a[$(sudo ls)]' id`, [][]string{sudoLs}, nil, nil, false},
		{`env -i - 'x=a[$(sudo ls)]' id`, [][]string{sudoLs}, nil, nil, false},
		{`nice env 'x=a[$(sudo ls)]' id`, [][]string{sudoLs}, nil, nil, false},
		{"env -S 'x=a[`sudo`] id'", [][]string{{"sudo"}}, nil, nil, false},
		{`strace -E 'x=a[$(sudo ls)]' bash -c '((x))'`, [][]string{sudoLs}, nil, nil, false},
		{`env $'x=a[\x24(sudo ls)]' bash -c '((x))'`, [][]string{sudoLs}, nil, nil, false},
		{`env 'x=$(sudo ls)' FOO=bar "A=$B" id`, nil, [][]string{sudoLs}, nil, false},
		// And so is that of for x in …, of ${x:=…} and one with expansions.
		{`for x in 'a[$(sudo ls)]'; do ((x)); done`, [][]string{sudoLs}, nil, nil, false},
		{`: ${x:=a[\$(sudo ls)]}; ((x))`, [][]string{sudoLs}, nil, nil, false},
		{`: "${x:=a[\$(sudo ls)]$y}"; ((x))`, [][]string{sudoLs}, nil, nil, false},
		{`x="a[\$(sudo ls)]$y"; ((x))`, [][]string{sudoLs}, nil, nil, false},
		{`x="a[\$(sudo ls)]"$y; ((x))`, [][]string{sudoLs}, nil, nil, false},
		{`[[ 'a[$(sudo ls)]'$y -eq 0 ]]`, [][]string{sudoLs}, nil, nil, false},
		{`x="a[$i]"; y=$((i+1)) z="${a[$i]}"; : ${w:=default}; for f in *.txt; do :; done`, nil, nil, nil, false},

		// $((…) …) is a substitution, ((…) …) a subshell.
		{`echo $((sudo ls) )`, [][]string{sudoLs}, nil, []string{"computed"}, true},
		{`((sudo ls) )`, [][]string{sudoLs}, nil, []string{"computed"}, true},
		{`((sudo -i) || (x))`, [][]string{{"sudo", "-i"}, {"x"}}, nil, []string{"computed", "stdin"}, true},
		{`echo $((false) || (sudo ls))`, [][]string{{"false"}, sudoLs}, nil, []string{"computed"}, true},
		{`echo "$((sudo ls) )"`, [][]string{sudoLs}, nil, []string{"computed"}, true},
		{"cat <<EOF\n$((sudo ls) )\nEOF", [][]string{sudoLs}, nil, []string{"computed"}, true},
		{`echo $(( $((sudo ls) ) + 1 ))`, [][]string{sudoLs}, nil, []string{"computed"}, true},
		{`echo $((sudo ls)|cat)`, [][]string{sudoLs, {"cat"}}, nil, []string{"computed"}, true},
		{`((sudo ls)|cat)`, [][]string{sudoLs, {"cat"}}, nil, []string{"computed"}, true},
		{`bash -c '((sudo ls) )'`, [][]string{sudoLs}, nil, []string{"computed"}, true},
		{`echo $(( (1+2) ) )`, [][]string{{"1+2"}}, nil, []string{"computed"}, true},
		{`((1) )`, [][]string{{"1"}}, nil, []string{"computed"}, true},
		// Arithmetic stays arithmetic.
		{`echo $((1 + 2)) $(( (1+2) )) $(((1)+(2)))`, nil, nil, nil, false},
		{`((i++)); (( (i) > 1 )); for ((i=0; (i)<2; i++)); do :; done`, nil, nil, nil, false},
	} {
		s, err := Parse(c.src, "", "")
		if (err != nil) != c.fails {
			t.Errorf("%s: error %v", c.src, err)
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

// With deny = ["sudo *"] the code these lines run is denied, a ${x@P} of
// a value the line does not give is asked about, and the lines without
// such code pass as before.
func TestLeftoversRules(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	e, err := Load(ctx, t.TempDir(), Rules{Deny: []string{"sudo *"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ cmd, want string }{
		{`y='$(sudo ls)'; echo "${y@P}"`, Deny},
		{`env 'x=a[$(sudo ls)]' bash -c '((x))'`, Deny},
		{`sudo 'x=a[$(sudo ls)]' id`, Deny},
		{`echo $((sudo ls) )`, Deny},
		{`((sudo ls) )`, Deny},
		{`x="a[\$(sudo ls)]$y"; ((x))`, Deny},
		{`echo "${PS1@P}"`, Ask},
		{`echo "${x@Q}"`, Allow},
		{`env FOO=bar ls`, Allow},
		{`echo $((1 + 2))`, Allow},
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

func TestPromptEscapes(t *testing.T) {
	for in, want := range map[string]string{
		`\044(id)`:      "$(id)",
		`\140id\140`:    "`id`",
		`a\nb`:          "a\nb",
		`\\$(id)`:       `\$(id)`,
		`\$(id)`:        `\$(id)`,
		`\44(id)`:       `\44(id)`,
		`\000(id)`:      `\000(id)`,
		`\0044`:         "\x044",
		`\w \u \e \[\]`: `\w \u \e \[\]`,
		`x\`:            `x\`,
	} {
		if got := promptEscapes(in); got != want {
			t.Errorf("promptEscapes(%q) = %q, want %q", in, got, want)
		}
	}
}
