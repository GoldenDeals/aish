package bashstate

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestKeywordRestore replays a change in a shell under set -k, where bash
// takes every NAME=VALUE word of a command for an assignment to its
// environment, the arguments of declare too: `declare -g X=1` would set
// nothing and list the shell's variables instead. The script restores
// variables, functions and aliases there, prints nothing, and leaves
// set -k as the session had it.
func TestKeywordRestore(t *testing.T) {
	const (
		base = `export KEEP=1; old() { echo old; }; alias ll='ls -l'`
		// Run before set -k: the user's own assignments are not the
		// script's to fix.
		session = `
export FOO=$'multi\nline "quoted"'
declare -a ARR=(1 "two words")
declare -A MAP=([k]=v [x]='y z')
declare -i NUM=42
declare -n REF=FOO
PLAIN=x
hello() { local greeting=hi; echo "$greeting $1"; }
unset -f old
alias gs='git status' ll='ls -la'
shopt -s globstar`
	)
	for _, tc := range []struct {
		name             string
		shellK, sessionK bool // set -k in the shell restored to, in the session
	}{
		{"both", true, true},
		{"session only", false, true},
		{"shell only", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k := func(on bool) string {
				if on {
					return "\nset -k"
				}
				return ""
			}
			b := dump(t, base+k(tc.shellK))
			s := dump(t, base+"\n"+session+k(tc.sessionK))
			for _, want := range []string{"FOO", "ARR", "MAP", "NUM", "REF", "PLAIN"} {
				if _, ok := s.Vars[want]; !ok {
					t.Fatalf("the session lacks %s: %v", want, keys(s.Vars))
				}
			}
			restored, out := replay(t, base+k(tc.shellK)+"\n_restore() {\n"+Script(Diff(b, s))+"\n}\n_restore")
			if out != "" {
				t.Errorf("the script printed %q", out)
			}
			if d := Diff(s, restored); !d.Empty() {
				t.Errorf("restored state differs: %+v", d)
			}
			if got, want := restored.Opts["keyword"], "set "+map[bool]string{true: "-", false: "+"}[tc.sessionK]+"o keyword"; got != want {
				t.Errorf("keyword after the script: %q, want %q", got, want)
			}
		})
	}
}

// replay runs script in a clean bash as dump does and returns the state it
// ends in and what it printed.
func replay(t *testing.T, script string) (State, string) {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "state")
	cmd := exec.Command("bash", "--norc", "--noprofile", "-c", dumpFunc(t)+"\n"+script+"\n__aish_dump >"+out)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=/nonexistent"}
	printed, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, printed)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	st, err := Parse(b, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	return st, string(printed)
}
