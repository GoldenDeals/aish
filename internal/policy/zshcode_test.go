package policy

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// zshCodeEnv is a directory with programs ls and rm in its bin, and the
// environment of a shell there, with SHELL set to shell unless it is "".
func zshCodeEnv(t *testing.T, shell string) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "bin"), 0o755)
	for _, prog := range []string{"ls", "rm"} {
		os.WriteFile(filepath.Join(dir, "bin", prog), []byte("#!/bin/sh\n"), 0o755)
	}
	env := []string{"HOME=" + dir, "PWD=" + dir, "PATH=" + filepath.Join(dir, "bin")}
	if shell != "" {
		env = append(env, "SHELL="+shell)
	}
	return dir, env
}

// handedIn is the input of line handed to the shell named shell, from dir
// with env.
func handedIn(shell, dir string, env []string, line string, opts ...string) Input {
	in := NewInputIn(shell, "bash", map[string]any{"command": line}, dir, env, opts...)
	in.HandOff(line)
	return in
}

// TestZshCodeUnderBash: the code a bash line hands to a zsh it names is
// read as a zsh reads it too, as a line of a zsh shell is, and so is the
// code that code hands on; that of bash stays bash's.
func TestZshCodeUnderBash(t *testing.T) {
	ctx := context.Background()
	e, err := Load(ctx, t.TempDir(), Rules{Deny: []string{"rm *"}})
	if err != nil {
		t.Fatal(err)
	}
	check := func(line string, env []string, dir string) Decision {
		t.Helper()
		d, err := e.Check(ctx, handedIn("bash", dir, env, line))
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	for _, shell := range []string{"", "/bin/bash", "/bin/zsh"} {
		dir, env := zshCodeEnv(t, shell)
		for _, line := range []string{
			`zsh -c 'noglob rm -rf x'`,
			`su -s /bin/zsh -c 'noglob rm -rf x'`,
			`su --shell=/usr/bin/zsh root -c 'noglob rm -rf x'`,
			`zsh -c 'echo =sudo'`,
			`zsh <<< 'noglob rm -rf x'`,
			`zsh -s <<'EOF'
noglob rm -rf x
EOF`,
			`/usr/local/bin/zsh5 -c 'noglob rm -rf x'`,
			`sudo zsh -fc 'noglob rm -rf x'`,
			`find . -exec zsh -c 'noglob rm -rf "$1"' _ {} \;`,
			`zsh -c 'eval "noglob rm -rf x"'`,
			`zsh -c 'bash -c "noglob rm -rf x"'`,
			`zsh -c 'ls *(e:"rm -rf ~":)'`,
			`zsh -c 'foreach f (x); rm -rf $f; end'`,
			// bash ran no code of these.
			`zsh5 -c 'rm -rf x'`,
			`zsh --emulate ksh -c 'rm -rf x'`,
			// The shell code programs run, and the rc files of a zsh.
			`SHELL=/bin/zsh sudo -s noglob rm -rf x`,
			`export SHELL=/bin/zsh`,
			`ZDOTDIR=/tmp/x zsh -c 'echo hi'`,
		} {
			if d := check(line, env, dir); d.Action == Allow {
				t.Errorf("SHELL=%s %q: allow, want ask or deny", shell, line)
			}
		}
		for _, line := range []string{
			`zsh -c 'echo hi'`,
			`zsh -c 'ls'`,
			`bash -c 'echo hi'`,
			`bash -c 'noglob rm -rf x'`, // a command named noglob to bash
			`sh -c 'ls *.go'`,
			`su -s /bin/bash -c 'noglob rm -rf x'`,
			`watch 'noglob rm -rf x'`,    // sh -c
			`ssh host 'noglob rm -rf x'`, // a shell over there is not known
			`echo zsh`,
		} {
			if d := check(line, env, dir); d.Action != Allow {
				t.Errorf("SHELL=%s %q: %+v, want allow", shell, line, d)
			}
		}
	}
}

// TestZshLogin: the code programs run with the shell of SHELL is a zsh's
// when SHELL names one, and bash's as before when it names another or is
// not set.
func TestZshLogin(t *testing.T) {
	lines := []string{
		`sudo -s noglob rm -rf x`,
		`sudo -s <<< 'noglob rm -rf x'`,
		`sudo -i noglob rm -rf x`,
		`doas -s <<< 'noglob rm -rf x'`,
		`su -c 'noglob rm -rf x'`,
		`su - root -c 'echo =sudo'`,
		`su <<< 'noglob rm -rf x'`,
		`script -qc 'noglob rm -rf x' /dev/null`,
		`script -q /dev/null <<< 'noglob rm -rf x'`,
		`flock /tmp/l -c 'noglob rm -rf x'`,
		`tmux new-window 'noglob rm -rf x'`,
		`at now <<< 'noglob rm -rf x'`,
	}
	for _, shell := range []string{"/bin/zsh", "/usr/bin/zsh-5.9"} {
		dir, env := zshCodeEnv(t, shell)
		for _, line := range lines {
			if got := handedIn("bash", dir, env, line).Dynamic; !slices.Contains(got, dynComputed) {
				t.Errorf("SHELL=%s %q: %v, want computed", shell, line, got)
			}
		}
		for _, line := range []string{`sudo -s echo hi`, `sudo -s ls`, `su -c 'echo hi'`, `bash -c 'echo $x[1]'`} {
			if got := handedIn("bash", dir, env, line).Dynamic; len(got) > 0 {
				t.Errorf("SHELL=%s %q: %v, want none", shell, line, got)
			}
		}
	}
	for _, shell := range []string{"", "/bin/bash"} {
		dir, env := zshCodeEnv(t, shell)
		for _, line := range lines[:7] {
			if got := handedIn("bash", dir, env, line).Dynamic; len(got) > 0 {
				t.Errorf("SHELL=%s %q: %v, want none", shell, line, got)
			}
		}
	}
}

// TestZshChild: a zsh a zsh starts has the options of its rc files, not
// those of the shell: any may be on.
func TestZshChild(t *testing.T) {
	dir, env := zshCodeEnv(t, "/bin/zsh")
	for _, c := range []struct {
		line string
		want bool
	}{
		{`ls *.go`, false},
		{`zsh -c 'ls *.go'`, true}, // nullglob
		{`ls a~b`, false},
		{`zsh -c 'ls a~b'`, true}, // extendedglob
		{`bash -c 'ls *.go'`, false},
		{`zsh -c 'echo hi'`, false},
	} {
		got := slices.Contains(handedIn("zsh", dir, env, c.line, zshDefaults...).Dynamic, dynComputed)
		if got != c.want {
			t.Errorf("%q: computed %v, want %v", c.line, got, c.want)
		}
	}
}
