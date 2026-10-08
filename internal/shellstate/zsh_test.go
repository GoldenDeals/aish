package shellstate

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/shellinit"
)

// zshDump runs script in a zsh without rc files, in a home of its own, and
// returns the state it ends in, as __aish_dump of init.zsh prints it.
func zshDump(t *testing.T, script string, ignore []string) State {
	t.Helper()
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("no zsh")
	}
	dump := regexp.MustCompile(`(?ms)^__aish_dump\(\) \{.*?^\}$`).FindString(shellinit.Zsh)
	if dump == "" {
		t.Fatal("no __aish_dump in init.zsh")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "state")
	cmd := exec.Command(zsh, "-f", "-c", "zmodload zsh/parameter\n"+dump+"\n"+script+"\n__aish_dump >"+out)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "ZDOTDIR=" + dir, "AISH_TOOLS_PATH=/run/aish-1/bin"}
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	st, err := ParseZsh(b, "", ignore)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// TestZshRoundTrip: what a session changed in a zsh — variables of each
// kind, special ones, a tied pair, functions, aliases of each kind,
// options — comes back in another, from the script sourced in a function,
// as __aish_precmd sources restore.bash.
func TestZshRoundTrip(t *testing.T) {
	base := `PATH=/run/aish-1/bin:$PATH; export KEEP=1 GONE=1; old() { echo old; }; alias ll='ls -l'; HISTSIZE=30`
	session := `
export FOO=$'multi\nline "quoted" it'\''s' GONE=
unset GONE
typeset -a ARR=(1 "two words" $'th\nree')
typeset -A MAP=([k]=v [x]='y z')
integer NUM=41
(( NUM++ ))
typeset -T LDP ldp=(/a /b)
typeset -gU UNIQ=(a b a)
hello() {
	cat <<EOF
}
EOF
	echo "hi $1"
}
'odd name' () { echo odd; }
unfunction old
alias gs='git status' ll='ls -la'
alias -g G='| head'
alias -s txt=less
setopt autocd extendedglob
unsetopt nomatch
_private=1
PATH=$PATH:/opt/session/bin
HISTSIZE=500
PS1='%~ %# '`
	b := zshDump(t, base, nil)
	s := zshDump(t, base+"\n"+session, nil)
	if b.Kind != Zsh || s.Kind != Zsh {
		t.Fatalf("kinds %q %q", b.Kind, s.Kind)
	}
	d := Diff(b, s)
	for _, want := range []string{"FOO", "ARR", "MAP", "NUM", "GONE", "PATH", "LDP", "UNIQ", "HISTSIZE", "PS1"} {
		if _, ok := d.Vars[want]; !ok {
			t.Errorf("diff misses variable %s: %v", want, keys(d.Vars))
		}
	}
	for _, gone := range []string{"_private", "SECONDS", "RANDOM", "ldp", "path", "status", "pipestatus", "ZSH_VERSION"} {
		if _, ok := d.Vars[gone]; ok {
			t.Errorf("%s should be ignored: %s", gone, d.Vars[gone])
		}
	}
	if strings.Contains(d.Vars["PATH"], "/run/aish-1/bin") {
		t.Errorf("PATH keeps the tools of this aish: %s", d.Vars["PATH"])
	}
	if strings.Contains(d.Vars["PS1"], "unset") || !strings.Contains(d.Vars["FOO"], "unset -v FOO") {
		t.Errorf("a special one is not unset, another one is: %q, %q", d.Vars["PS1"], d.Vars["FOO"])
	}
	if d.Funcs["old"] != "" || d.Funcs["hello"] == "" || d.Funcs["odd name"] == "" {
		t.Errorf("funcs %v", d.Funcs)
	}
	if d.Aliases["-g G"] != "| head" || d.Aliases["-s txt"] != "less" || d.Aliases["gs"] != "git status" {
		t.Errorf("aliases %v", d.Aliases)
	}
	if d.Opts["autocd"] != "setopt autocd" || d.Opts["nomatch"] != "unsetopt nomatch" {
		t.Errorf("opts %v", d.Opts)
	}

	// Replay the change in a fresh shell, whose own tools come first in
	// PATH, with options that would read the script otherwise.
	script := strings.ReplaceAll(Script(d), "AISH_TOOLS_PATH", "AISH_NEW_TOOLS")
	f := filepath.Join(t.TempDir(), "restore")
	if err := os.WriteFile(f, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	user := "setopt rcquotes ignorebraces kshglob shwordsplit ksharrays nounset warncreateglobal; alias -g EOF='| rm -rf /nonexistent'; alias echo=false\n"
	restored := zshDump(t, "AISH_NEW_TOOLS=/run/aish-2/bin\n"+base+"\n"+user+"_restore() { source "+f+"; }\n_restore\nunalias echo; unalias EOF; unsetopt rcquotes ignorebraces kshglob shwordsplit ksharrays nounset warncreateglobal", nil)
	if again := Diff(s, restored); !onlyPath(again) {
		t.Errorf("restored state differs: %+v\nscript:\n%s", again, script)
	}
	if !strings.Contains(restored.Vars["PATH"], "path=( /run/aish-2/bin ") {
		t.Errorf("PATH %s", restored.Vars["PATH"])
	}
	if !strings.Contains(restored.Funcs["hello"], "EOF\n}\nEOF") {
		t.Errorf("here-document lost: %s", restored.Funcs["hello"])
	}
}

// TestZshStateIgnore: state_ignore leaves zsh's variables out as bash's.
func TestZshStateIgnore(t *testing.T) {
	st := zshDump(t, `export FOO_TOKEN=x KEEP=1`, []string{"*TOKEN*"})
	if _, ok := st.Vars["FOO_TOKEN"]; ok {
		t.Error("FOO_TOKEN should be ignored")
	}
	if _, ok := st.Vars["KEEP"]; !ok {
		t.Error("KEEP should be kept")
	}
}

// TestZshOn: the options a zsh has on are named as zsh names them.
func TestZshOn(t *testing.T) {
	st := zshDump(t, `setopt extendedglob`, nil)
	on := st.On()
	if !contains(on, "extendedglob") || contains(on, "nomatch") == false || contains(on, "rcquotes") {
		t.Errorf("on: %v", on)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
