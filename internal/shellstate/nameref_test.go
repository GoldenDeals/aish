package shellstate

import "testing"

// TestNamerefRestore replays the change from a shell to another state in
// that shell, as switching sessions does, where a name of the change is a
// nameref in the shell or in the state: the script takes the nameref off
// or puts it on by itself, never the variable it refers to, which the
// script has set or left as the state has it. Plain variables go and
// change as they did.
func TestNamerefRestore(t *testing.T) {
	for _, tc := range []struct{ name, shell, state string }{
		{"nameref gone", `FOO=1; declare -n REF=FOO`, `FOO=1`},
		{"nameref gone, its variable set", `FOO=2; declare -n REF=FOO`, `FOO=1`},
		{"nameref gone, its variable unset", `declare -n REF=FOO`, ``},
		{"nameref without a value gone", `FOO=1; declare -n REF`, `FOO=1`},
		{"nameref to a plain variable", `FOO=1; declare -n REF=FOO`, `FOO=1; REF=x`},
		{"nameref to another", `FOO=1; BAR=2; declare -n REF=FOO`, `FOO=1; BAR=2; declare -n REF=BAR`},
		{"plain variable to a nameref", `FOO=1; REF=x`, `FOO=1; declare -n REF=FOO`},
		{"nameref first in order", `ZED=1; declare -n AREF=ZED`, `ZED=2`},
		{"plain variable gone", `FOO=1; GONE=1`, `FOO=1`},
		{"plain variable set", `FOO=1`, `FOO=2; NEW=3`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cur, want := dump(t, tc.shell), dump(t, tc.state)
			restored, out := replay(t, tc.shell+"\n_restore() {\n"+Script(Diff(cur, want))+"\n}\n_restore")
			if out != "" {
				t.Errorf("the script printed %q", out)
			}
			if d := Diff(want, restored); !d.Empty() {
				t.Errorf("restored state differs: %+v\nscript:\n%s", d, Script(Diff(cur, want)))
			}
		})
	}
}

// TestReadonlyNameref: a readonly nameref the script cannot take off stays,
// and neither unset nor an assignment goes through it to its variable.
func TestReadonlyNameref(t *testing.T) {
	const shell = `FOO=1; declare -rn REF=FOO`
	cur, want := dump(t, shell), dump(t, `FOO=1; REF=x`)
	restored, _ := replay(t, shell+"\n_restore() {\n"+Script(Diff(cur, want))+"\n}\n_restore")
	if got := restored.Vars["FOO"]; got != want.Vars["FOO"] {
		t.Errorf("FOO %q, want %q", got, want.Vars["FOO"])
	}
}
