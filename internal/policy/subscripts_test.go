package policy

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// TestParseSubscripts checks the strings bash evaluates as it runs: the
// code in a subscript of a name given as text, in a value read as
// arithmetic later, in single quotes where bash expands as in double
// quotes, and in a compound assignment declare takes from a string, is
// among the commands. has lists argv that must be among them, not those
// that must not; dynamic is the whole list of marks.
func TestParseSubscripts(t *testing.T) {
	sudoLs := []string{"sudo", "ls"}
	for _, c := range []struct {
		src     string
		has     [][]string
		not     [][]string
		dynamic []string
	}{
		// Names given as text.
		{`let 'a[$(sudo ls)]=1'`, [][]string{sudoLs}, nil, nil},
		{"let 'a[`sudo ls`]=1'", [][]string{sudoLs}, nil, nil},
		{`let 'a[${x:-$(sudo ls)}]=1'`, [][]string{sudoLs}, nil, nil},
		{`let "a[\$(sudo ls)]=1" i++`, [][]string{sudoLs}, nil, nil},
		{`let a['$(sudo ls)']=1`, [][]string{sudoLs}, nil, nil},
		{`let a[\$\(sudo\ ls\)]=1`, [][]string{sudoLs}, nil, nil},
		{`let x='a[$(sudo ls)]'`, [][]string{sudoLs}, nil, nil},
		{`let $'a[\x24(sudo ls)]=1'`, [][]string{sudoLs}, nil, nil},
		{`builtin let 'a[$(sudo ls)]=1'`, [][]string{sudoLs}, nil, nil},
		{`[[ -v 'a[$(sudo ls)]' ]]`, [][]string{sudoLs}, nil, nil},
		{`[[ -v $'a[\x24(sudo ls)]' ]]`, [][]string{sudoLs}, nil, nil},
		{`[[ -n x && 'a[$(sudo ls)]' -eq 0 ]]`, [][]string{sudoLs}, nil, nil},
		{`test -v 'a[$(sudo ls)]'`, [][]string{sudoLs}, nil, nil},
		{`[ -v 'a[$(sudo ls)]' ]`, [][]string{sudoLs}, nil, nil},
		{`test -v $'a[\x24(sudo ls)]'`, [][]string{sudoLs}, nil, nil},
		{`test "$op" 'a[$(sudo ls)]'`, [][]string{sudoLs}, nil, nil},
		{`declare -A a; unset 'a[$(sudo ls)]'`, [][]string{sudoLs}, nil, nil},
		{`unset -v x 'a[$(sudo ls)]'`, [][]string{sudoLs}, nil, nil},
		// The marks of 132 stay: what the code runs is told besides.
		{`declare -n r='a[$(sudo ls)]'`, [][]string{sudoLs}, nil, []string{"computed"}},
		{`local -n r='a[$(sudo ls)]'`, [][]string{sudoLs}, nil, []string{"computed"}},
		{`read 'a[$(sudo ls)]' < f`, [][]string{sudoLs}, nil, []string{"computed"}},
		{`printf -v 'a[$(sudo ls)]' 1`, [][]string{sudoLs}, nil, []string{"computed"}},
		{`declare 'a[$(sudo ls)]=1'`, [][]string{sudoLs}, nil, []string{"computed"}},

		// Values read as arithmetic later.
		{`x='a[$(sudo ls)]'; (( x ))`, [][]string{sudoLs}, nil, nil},
		{`x="a[\$(sudo ls)]"`, [][]string{sudoLs}, nil, nil},
		{`x=a[\$\(sudo\ ls\)]`, [][]string{sudoLs}, nil, nil},
		{`x=$'a[\x24(sudo ls)]'`, [][]string{sudoLs}, nil, nil},
		{`x+='b[0] + a[$(sudo ls)]'`, [][]string{sudoLs}, nil, nil},
		{`b[0]='a[$(sudo ls)]'`, [][]string{sudoLs}, nil, nil},
		{`a=('b[$(sudo ls)]')`, [][]string{sudoLs}, nil, nil},
		{`declare -i n='a[$(sudo ls)]'`, [][]string{sudoLs}, nil, nil},
		{`export x='a[$(sudo ls)]'`, [][]string{sudoLs}, nil, nil},
		{`builtin readonly 'x=a[$(sudo ls)]'`, [][]string{sudoLs}, nil, nil},
		{`x='a[$(sudo ls)]' bash -c '((x))'`, [][]string{sudoLs}, nil, nil},
		{`x='a[$(]'`, nil, nil, []string{"computed"}},

		// Single quotes where bash expands as in double quotes.
		{`(( 'a[$(sudo ls)]' ))`, [][]string{sudoLs}, nil, nil},
		{`(( '$(sudo ls)' ))`, [][]string{sudoLs}, nil, nil},
		{`(( $'\x24(sudo ls)' ))`, [][]string{sudoLs}, nil, nil},
		{`echo $(( 1 + 'a[$(sudo ls)]' )) $[ '$(sudo ls)' ]`, [][]string{sudoLs}, nil, nil},
		{`for (( i = '$(sudo ls)'; i < 1; i++ )); do :; done`, [][]string{sudoLs}, nil, nil},
		{`echo ${a['$(sudo ls)']}`, [][]string{sudoLs}, nil, nil},
		{`echo "${s:'$(sudo ls)':1}"`, [][]string{sudoLs}, nil, nil},
		{`a['$(sudo ls)']=1`, [][]string{sudoLs}, nil, nil},
		{`a=(['$(sudo ls)']=1)`, [][]string{sudoLs}, nil, nil},
		{`(( ${x:-'$(sudo ls)'} ))`, [][]string{sudoLs}, nil, nil},
		{`echo "${x:-'$(sudo ls)'}" "${y:='$(sudo ls)'}"`, [][]string{sudoLs}, nil, nil},
		{`cat <<< "${x:+'$(sudo ls)'}"`, [][]string{sudoLs}, nil, nil},
		{"cat <<EOF\n${x:-'$(sudo ls)'}\nEOF", [][]string{{"cat"}, sudoLs}, nil, nil},
		{"cat <<EOF\n$(( 'a[$(sudo ls)]' ))\nEOF", [][]string{{"cat"}, sudoLs}, nil, nil},
		// Elsewhere they quote.
		{`echo ${x:-'$(sudo ls)'} "${x#'$(sudo ls)'}" "${x/'$(sudo ls)'/y}"`, nil, [][]string{sudoLs}, nil},
		{"cat <<'EOF'\n${x:-'$(sudo ls)'}\nEOF", nil, [][]string{sudoLs}, nil},
		{`(( $(echo '$(sudo ls)') ))`, [][]string{{"echo", "$(sudo ls)"}}, [][]string{sudoLs}, nil},
		{`echo '$(sudo ls)' 'a[$(sudo ls)]'; [ -n 'a[$(sudo ls)]' ]`, nil, [][]string{sudoLs}, nil},

		// Compound assignments from a string.
		{`declare -a a='($(sudo ls))'`, [][]string{sudoLs}, nil, nil},
		{`declare -a a='([0]=$(sudo ls))'`, [][]string{sudoLs}, nil, nil},
		{`local -a a='(<(sudo ls))'`, [][]string{sudoLs}, nil, nil},
		{`declare -a 'a=($(sudo ls))'`, [][]string{sudoLs}, nil, nil},
		{`builtin declare -a 'a=(x $(sudo ls))'`, [][]string{sudoLs}, nil, nil},
		{"declare -a a='(`sudo ls`)'", [][]string{sudoLs}, nil, nil},
		{`declare -a a=' ($(sudo ls))'; x='($(sudo ls))'`, nil, [][]string{sudoLs}, nil},

		// The code of a string in code is parsed as that code's.
		{`eval "let 'a[\$(sudo ls)]=1'"`, [][]string{sudoLs}, nil, nil},
		{`bash -c 'x="a[\$(sudo ls)]"; ((x))'`, [][]string{sudoLs}, nil, nil},
		{`let 'a[$(eval "sudo ls")]=1'`, [][]string{{"eval", "sudo ls"}, sudoLs}, nil, nil},

		// A subscript with an expansion of the line that let and test -v
		// expand again: what it gives runs. Braces may make $( of $,x.
		{`let "a[$i]=1"`, nil, nil, []string{"computed"}},
		{`let a[$i]++`, nil, nil, []string{"computed"}},
		{`test -v "a[$i]"`, nil, nil, []string{"computed"}},
		{`builtin let a[{\$,x}\(sudo\ ls\)]=1`, nil, nil, []string{"computed"}},

		// What bash does not evaluate again, or holds no code.
		{`let 'a[{$,x}(sudo ls)]=1'`, nil, [][]string{sudoLs}, nil},
		{`let i++ 'x = y + 1' "n=$n+1" "x=${a[$i]}+1" "a[i]+=$n"`, nil, nil, nil},
		{`unset 'a[1]' x; unset -f 'a[$(sudo ls)]'`, nil, [][]string{sudoLs}, nil},
		{`[[ -v a[0] ]]; [[ -v a[$i] ]]; [[ -v "$x" ]]; [ -v "$x" ]; test -v x`, nil, nil, nil},
		{`x=$((i+1)); msg='hello'; (( a[$i]++ )); echo "${a[$i]}" "${s:i:1}"`, nil, nil, nil},
		{`[ "${arr[$i]}" = x ]; [[ $x -eq 0 ]]; [[ ${#a[@]} -gt 1 ]]`, nil, nil, nil},
		{`re='^[0-9]+$'; j='{"a":[1,2]}'; awk='{ a[$1]++ }'; sql='a[1] = ${x}'`, nil, nil, nil},
		{`RED=$'\033[31m'; a=(x 'y z'); declare -A m=([k]=v); declare -a a='(1 2)'`, nil, nil, nil},
		{`x='a[$((1+2))]'; x='a[${#b[@]}]'; echo "${x:-'default'}"`, nil, nil, nil},
	} {
		s, err := Parse(c.src, "", "")
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

// The code in the subscripts runs on the machine of the line that holds
// them, and is nested as any code is.
func TestParseSubscriptsNested(t *testing.T) {
	s, err := Parse(`ssh box "let 'a[\$(sudo ls)]=1'"`, "/", "/home/me")
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(s.Commands, func(a []string) bool { return slices.Equal(a, []string{"sudo", "ls"}) })
	if i < 0 || !slices.Contains(s.Remote, i) {
		t.Errorf("commands %q, remote %v: sudo ls must run over there", s.Commands, s.Remote)
	}
	// let at the depth of maxDepth: the code of its subscript is deeper.
	src := `let 'a[$(sudo ls)]=1'`
	for range maxDepth {
		src = "eval '" + strings.ReplaceAll(src, "'", `'\''`) + "'"
	}
	s, err = Parse(src, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(s.Dynamic, "depth") {
		t.Errorf("%s: dynamic %q, code past maxDepth must be marked", src, s.Dynamic)
	}
}

// With deny = ["sudo *"] the code of the strings bash evaluates is denied
// as written out, and the lines without such code pass as before.
func TestSubscriptsRules(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	e, err := Load(ctx, t.TempDir(), Rules{Deny: []string{"sudo *"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ cmd, want string }{
		{`let 'a[$(sudo ls)]=1'`, Deny},
		{`[[ -v 'a[$(sudo ls)]' ]]`, Deny},
		{`test -v 'a[$(sudo ls)]'`, Deny},
		{`[ -v 'a[$(sudo ls)]' ]`, Deny},
		{`declare -A a; unset 'a[$(sudo ls)]'`, Deny},
		{`x='a[$(sudo ls)]'; (( x ))`, Deny},
		{`declare -n r='a[$(sudo ls)]'`, Deny},
		{`(( 'a[$(sudo ls)]' ))`, Deny},
		{`declare -a a='($(sudo ls))'`, Deny},
		{`echo "${x:-'$(sudo ls)'}"`, Deny},
		{`let "a[$i]=1"`, Ask},
		{`let i++`, Allow},
		{`unset 'a[1]'`, Allow},
		{`[[ -v a[0] ]]`, Allow},
		{`x=$((i+1))`, Allow},
		{`msg='hello'`, Allow},
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

func TestAnsiC(t *testing.T) {
	for in, want := range map[string]string{
		`a[\x24(id)]`:      "a[$(id)]",
		`\044(id)`:         "$(id)",
		`\u0024\U00000060`: "$`",
		`\e[31m\t\'\\`:     "\x1b[31m\t'\\",
		`\cA\x\q\`:         "\x01\\x\\q\\",
	} {
		if got := ansiC(in); got != want {
			t.Errorf("ansiC(%q) = %q, want %q", in, got, want)
		}
	}
}
