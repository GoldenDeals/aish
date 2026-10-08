package shells

import (
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/shellstate"
)

// TestFor: shell in config.toml picks the shell by the name of its
// program; one aish has no integration for is an error, not a bash run
// with the arguments of another.
func TestFor(t *testing.T) {
	for _, tc := range []struct{ configured, want string }{
		{"", "bash"},
		{"bash", "bash"},
		{"/opt/bash/bin/bash", "bash"},
		{"zsh", "zsh"},
		{"/usr/local/bin/zsh-5.9", "zsh"},
		{"mybash", "bash"}, // any other name, as before there was zsh
	} {
		sh, err := For(tc.configured)
		if err != nil || sh.Name() != tc.want {
			t.Errorf("%q: %v, %v; want %s", tc.configured, sh, err, tc.want)
		}
		if got := Kind(tc.configured); got != tc.want {
			t.Errorf("Kind(%q) = %s, want %s", tc.configured, got, tc.want)
		}
	}
	for _, bad := range []string{"fish", "/usr/bin/fish"} {
		if _, err := For(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

// TestRestoreOtherKind: the state of another shell is code this one does
// not read; only its directory comes back.
func TestRestoreOtherKind(t *testing.T) {
	bash := shellstate.State{Vars: map[string]string{"X": `declare -- X="1"`}, Cwd: "/srv"}
	zsh := shellstate.State{Kind: shellstate.Zsh, Vars: map[string]string{"X": "typeset -g X=1"}, Cwd: "/srv"}
	for name, got := range map[string]string{"bash in zsh": Zsh{}.RestoreScript(bash), "zsh in bash": Bash{}.RestoreScript(zsh)} {
		if strings.Contains(got, "X=") || !strings.Contains(got, "/srv") {
			t.Errorf("%s:\n%s", name, got)
		}
	}
	if got := (Zsh{}).RestoreScript(zsh); !strings.Contains(got, "typeset -g X=1") {
		t.Errorf("zsh in zsh:\n%s", got)
	}
}

// TestZshCommand: zsh starts with aish's ZDOTDIR, the user's in
// AISH_ZDOTDIR for .zshenv there to put back, and only when there was one.
func TestZshCommand(t *testing.T) {
	sh := Zsh{Path: "/bin/sh"} // any program: Command does not run it
	for _, tc := range []struct {
		env  []string
		want []string
	}{
		{[]string{"A=1", "ZDOTDIR=/home/u/.config/zsh"}, []string{"A=1", "AISH_ZDOTDIR=/home/u/.config/zsh", "ZDOTDIR=/run/a/zsh"}},
		{[]string{"A=1", "AISH_ZDOTDIR=/stale"}, []string{"A=1", "ZDOTDIR=/run/a/zsh"}},
	} {
		cmd, err := sh.Command("/run/a", tc.env)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(cmd.Env, " ") != strings.Join(tc.want, " ") || cmd.Args[len(cmd.Args)-1] != "-i" {
			t.Errorf("%v: %v %v", tc.env, cmd.Args, cmd.Env)
		}
	}
	files := Zsh{}.Files()
	if files["zsh/.zshenv"] == "" || !strings.Contains(files["zsh/.zshrc"], "__aish_route") {
		t.Errorf("files %v", files)
	}
}
