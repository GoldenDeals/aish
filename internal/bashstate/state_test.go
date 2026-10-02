package bashstate

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/inebotov/aish/internal/shellinit"
)

// dumpFunc is __aish_dump as init.bash defines it.
func dumpFunc(t *testing.T) string {
	m := regexp.MustCompile(`(?ms)^__aish_dump\(\) \{.*?^\}$`).FindString(shellinit.Bash)
	if m == "" {
		t.Fatal("no __aish_dump in init.bash")
	}
	return m
}

// dump runs script in a clean bash and returns the state it ends in.
func dump(t *testing.T, script string) State {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "state")
	cmd := exec.Command("bash", "--norc", "--noprofile", "-c", dumpFunc(t)+"\n"+script+"\n__aish_dump >"+out)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=/nonexistent", "AISH_TOOLS_PATH=/run/aish-1/bin"}
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	st, err := Parse(b, "")
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestRoundTrip(t *testing.T) {
	base := `PATH=/run/aish-1/bin:$PATH; export KEEP=1 GONE=1; old() { echo old; }; alias ll='ls -l'`
	session := `
export FOO=$'multi\nline "quoted"' GONE=
unset GONE
declare -a ARR=(1 "two words" $'th\nree')
declare -A MAP=([k]=v [x]='y z')
declare -i NUM=41
NUM+=1
hello() {
	cat <<EOF
}
EOF
	echo "hi $1"
}
unset -f old
alias gs='git status' ll='ls -la'
alias multi=$'echo a\necho b'
shopt -s globstar
set -o noclobber
_private=1
PATH=$PATH:/opt/session/bin`
	b := dump(t, base)
	s := dump(t, base+"\n"+session)
	d := Diff(b, s)
	for _, want := range []string{"FOO", "ARR", "MAP", "NUM", "GONE", "PATH"} {
		if _, ok := d.Vars[want]; !ok {
			t.Errorf("diff misses variable %s: %v", want, keys(d.Vars))
		}
	}
	if _, ok := d.Vars["_private"]; ok {
		t.Error("_private should be ignored")
	}
	if strings.Contains(d.Vars["PATH"], "/run/aish-1/bin") {
		t.Errorf("PATH keeps the tools of this aish: %s", d.Vars["PATH"])
	}
	if d.Funcs["old"] != "" || d.Funcs["hello"] == "" {
		t.Errorf("funcs %v", d.Funcs)
	}

	// Replay the change in a fresh shell, whose own tools come first in PATH.
	script := strings.ReplaceAll(Script(d), "AISH_TOOLS_PATH", "AISH_NEW_TOOLS")
	restored := dump(t, "AISH_NEW_TOOLS=/run/aish-2/bin\n"+base+"\n_restore() {\n"+script+"\n}\n_restore")
	if again := Diff(s, restored); !onlyPath(again) {
		t.Errorf("restored state differs: %+v", again)
	}
	if !strings.Contains(restored.Vars["PATH"], `="/run/aish-2/bin:`) {
		t.Errorf("PATH %s", restored.Vars["PATH"])
	}
	if !strings.Contains(restored.Funcs["hello"], "EOF\n}\nEOF") {
		t.Errorf("here-document lost: %s", restored.Funcs["hello"])
	}
}

// onlyPath: the restored PATH has the new tools directory in front, which
// Parse does not strip (AISH_TOOLS_PATH is renamed in the test).
func onlyPath(d State) bool {
	return len(d.Funcs)+len(d.Aliases)+len(d.Opts) == 0 && len(d.Vars) == 1 && d.Vars["PATH"] != ""
}

func TestApply(t *testing.T) {
	base := State{Vars: map[string]string{"A": "declare -- A=\"1\"", "B": "declare -- B=\"1\""}, Cwd: "/x"}
	d := State{Vars: map[string]string{"A": "", "C": "declare -- C=\"3\""}, Cwd: "/y"}
	got := Apply(base, d)
	if _, ok := got.Vars["A"]; ok || got.Vars["C"] == "" || got.Vars["B"] == "" || got.Cwd != "/y" {
		t.Errorf("%+v", got)
	}
	if d := Diff(base, base); len(d.Vars) != 0 || d.Cwd != "/x" {
		t.Errorf("a state differs from itself only by keeping the directory: %+v", d)
	}
}
