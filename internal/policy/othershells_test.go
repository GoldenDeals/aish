package policy

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestMain points passwdFile at a database whose root has bash, as the
// tests of su and sudo -i expect, whatever this machine's root has.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "policy")
	if err != nil {
		panic(err)
	}
	passwdFile = filepath.Join(dir, "passwd")
	if err := os.WriteFile(passwdFile, []byte("root:x:0:0:root:/root:/bin/bash\n"), 0o600); err != nil {
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// withPasswd points passwdFile at a database of lines for the test.
func withPasswd(t *testing.T, lines string) {
	t.Helper()
	old := passwdFile
	passwdFile = filepath.Join(t.TempDir(), "passwd")
	if err := os.WriteFile(passwdFile, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { passwdFile = old })
}

func TestShellKind(t *testing.T) {
	for prog, want := range map[string]int{
		"bash": bashShell, "/bin/sh": bashShell, "bash5.2": bashShell, "bash-5.2.37": bashShell, "rbash": bashShell,
		"zsh": zshKind, "zsh-5.9": zshKind, "/bin/rzsh": zshKind,
		"ksh": posixShell, "ksh93": posixShell, "/usr/bin/mksh-R59": posixShell, "pdksh": posixShell, "oksh": posixShell,
		"yash": posixShell, "ash": posixShell, "posh": posixShell,
		"fish": foreignShell, "tcsh": foreignShell, "csh": foreignShell, "rc": foreignShell, "es": foreignShell,
		"xonsh": foreignShell, "elvish": foreignShell, "nu": foreignShell, "/usr/bin/pwsh-preview": foreignShell,
		"fish4": foreignShell, "sha256sum": notShell, "ssh": notShell, "ionice": notShell, "nuget": notShell, "rcs": notShell,
		"fish_indent": notShell, "esbuild": notShell, "shred": notShell, "dashboard": notShell, "tclsh": notShell,
	} {
		if got := shellKind(prog); got != want {
			t.Errorf("%s: kind %d, want %d", prog, got, want)
		}
	}
}

// TestOtherShellsByName: the code of a shell of the ksh and ash kin, or a
// bash by a versioned name, is read as bash's; that of a shell of another
// language is computed, and found as bash would find it all the same.
func TestOtherShellsByName(t *testing.T) {
	ctx := context.Background()
	e, err := Load(ctx, t.TempDir(), Rules{Deny: []string{"rm *"}})
	if err != nil {
		t.Fatal(err)
	}
	dir, env := zshCodeEnv(t, "/bin/bash")
	check := func(line string) Input {
		t.Helper()
		return handedIn("bash", dir, env, line)
	}
	decide := func(in Input) Decision {
		t.Helper()
		d, err := e.Check(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	for _, line := range []string{
		`ksh -c 'rm -rf x'`,
		`mksh -c 'rm -rf x'`,
		`bash5.2 -c 'rm -rf x'`,
		`bash-5.2 -c 'rm -rf x'`,
		`yash -c 'rm -rf x'`,
		`ksh93 -c 'rm -rf x'`,
		`/usr/bin/mksh-R59 -c 'rm -rf x'`,
		`pdksh -c 'rm -rf x'`,
		`oksh -ec 'rm -rf x'`,
		`posh -c 'rm -rf x'`,
		`ash -c 'rm -rf x'`,
		`busybox ash -c 'rm -rf x'`,
		`busybox hush -c 'rm -rf x'`,
		`sudo ksh -c 'rm -rf x'`,
		`ksh <<< 'rm -rf x'`,
		`find . -exec mksh -c 'rm -rf "$1"' _ {} \;`,
		`fish -c 'rm -rf x'`,
		`tcsh -c 'rm -rf x'`,
	} {
		if d := decide(check(line)); d.Action != Deny {
			t.Errorf("%q: %+v, want deny", line, d)
		}
	}
	for _, line := range []string{
		`fish -c 'rm -rf x'`,
		`tcsh -c 'rm -rf x'`,
		`csh -fc 'rm -rf x'`,
		`rc -c 'rm -rf x'`,
		`es -c 'rm -rf x'`,
		`xonsh -c 'rm -rf x'`,
		`elvish -c 'rm -rf x'`,
		`nu -c 'rm -rf x'`,
		`nu --commands 'rm -rf x'`,
		`fish --command='rm -rf x'`,
		`fish -C 'rm -rf x'`,
		`fish -i -c 'rm -rf x'`,
		`/usr/bin/fish -c 'rm -rf x'`,
		`fish <<< 'rm -rf x'`,
		`unbuffer fish -c 'rm -rf x'`,
		`unbuffer /usr/local/bin/nu -c 'rm -rf x'`,
		`sudo -u bob fish -c 'rm -rf x'`,
		`pwsh -Command 'Remove-Item -Recurse x'`,
		`su -s /usr/bin/fish -c 'ls'`,
		// Options the policy does not know may take the word after them.
		`mksh -T /dev/tty2 -c 'rm -rf x'`,
		`ksh93 -R db -c 'ls'`,
		`yash --rcfile x -c 'ls'`,
		`ksh "$o" -c 'ls'`,
		`fish "$f"`,
	} {
		in := check(line)
		if !slices.Contains(in.Dynamic, dynComputed) {
			t.Errorf("%q: %v, want computed", line, in.Dynamic)
		}
		if d := decide(in); d.Action == Allow {
			t.Errorf("%q: allow", line)
		}
	}
	if in := check(`echo 'rm -rf x' | fish`); !slices.Contains(in.Dynamic, dynStdin) {
		t.Errorf("fish on stdin: %v, want stdin", in.Dynamic)
	}
	for _, line := range []string{
		`ksh -c 'echo hi'`,
		`mksh -c ls`,
		`yash -o errexit -c ls`,
		`bash5.2 -c 'echo hi'`,
		`fish script.fish`,
		`echo fish tcsh rc`,
		`fish_indent -w x.fish`,
		`sha256sum x`,
		`ionice -c3 ls`,
		`screen -c /tmp/rc -dm sleep 1`,
		`nuget list`,
	} {
		in := check(line)
		if len(in.Dynamic) > 0 && !slices.Equal(in.Dynamic, []string{dynSource}) {
			t.Errorf("%q: %v, want none", line, in.Dynamic)
		}
		if d := decide(in); d.Action != Allow && !slices.Contains(in.Dynamic, dynSource) {
			t.Errorf("%q: %+v, want allow", line, d)
		}
	}
}

// TestLoginShellOfTarget: su, runuser, sudo -i and run0 run code with the
// login shell of the user they run it as, which the user database names,
// besides the shell of SHELL; a user it does not have has a shell the
// policy does not know.
func TestLoginShellOfTarget(t *testing.T) {
	withPasswd(t, "+::::::\nroot:x:0:0::/root:/usr/bin/zsh\nbob:x:1000:1000::/home/bob:/usr/bin/fish\n"+
		"alice:x:1001:1001::/home/alice:/bin/bash\ncarol:x:1002:1002::/home/carol:\n")
	ctx := context.Background()
	e, err := Load(ctx, t.TempDir(), Rules{Deny: []string{"rm *"}})
	if err != nil {
		t.Fatal(err)
	}
	dir, env := zshCodeEnv(t, "/bin/bash")
	dynamic := func(line string) []string {
		t.Helper()
		return handedIn("bash", dir, env, line).Dynamic
	}
	for _, line := range []string{
		// root has zsh.
		`su -c 'noglob rm -rf x'`,
		`su - -c 'noglob rm -rf x'`,
		`su root -c 'noglob rm -rf x'`,
		`su -l root -c 'noglob rm -rf x'`,
		`su root -- -c 'noglob rm -rf x'`,
		`su <<< 'noglob rm -rf x'`,
		`runuser -l root -c 'noglob rm -rf x'`,
		`sudo -i noglob rm -rf x`,
		`sudo -i <<< 'noglob rm -rf x'`,
		`sudo --login -u root noglob rm -rf x`,
		`sudo -u '#0' -i noglob rm -rf x`,
		`run0 -i noglob rm -rf x`,
		`run0 --via-shell noglob rm -rf x`,
		`run0 <<< 'noglob rm -rf x'`,
		`sudo su -c 'noglob rm -rf x'`,
		// bob has fish.
		`su bob -c 'ls'`,
		`su - bob -c 'ls'`,
		`sudo -i -u bob ls`,
		`run0 -u bob --via-shell ls`,
		// Nobody knows the shell of dave, nor whose that of "$u" is.
		`su dave -c 'ls'`,
		`sudo -i -u dave ls`,
		`sudo -i -u "$u" ls`,
		`run0 --user=dave -i ls`,
	} {
		if got := dynamic(line); !slices.Contains(got, dynComputed) {
			t.Errorf("%q: %v, want computed", line, got)
		}
	}
	if d, _ := e.Check(ctx, handedIn("bash", dir, env, `su -c 'noglob rm -rf x'`)); d.Action == Allow {
		t.Errorf("su -c with root's zsh: allow")
	}
	for _, line := range []string{
		`su -c 'ls'`, // zsh reads it as bash does
		`su alice -c 'noglob rm -rf x'`,
		`sudo -i -u alice noglob rm -rf x`,
		`sudo -u '#1001' -i ls`,
		`su carol -c 'noglob ls'`, // /bin/sh
		`su -s /bin/bash -c 'noglob rm -rf x'`,
		`sudo -s noglob rm -rf x`, // the shell of SHELL
		`doas -s <<< 'noglob rm -rf x'`,
		`sudo -u bob ls`,
		`runuser -u dave -- ls`,
		`echo id | su dave`, // stdin, no code to read
	} {
		if got := dynamic(line); slices.Contains(got, dynComputed) {
			t.Errorf("%q: %v, want no computed", line, got)
		}
	}
	// SHELL is read besides the user database, as before.
	dir, env = zshCodeEnv(t, "/bin/zsh")
	if got := handedIn("bash", dir, env, `su alice -c 'noglob rm -rf x'`).Dynamic; !slices.Contains(got, dynComputed) {
		t.Errorf("SHELL=/bin/zsh, su alice: %v, want computed", got)
	}
	// No database at all.
	passwdFile = filepath.Join(t.TempDir(), "none")
	if got := handedIn("bash", dir, env, `su -c 'ls'`).Dynamic; !slices.Contains(got, dynComputed) {
		t.Errorf("no passwd: %v, want computed", got)
	}
}

// TestForeignLogin: SHELL of another language runs the code programs run
// with it, which is computed; a program that hands it none is not.
func TestForeignLogin(t *testing.T) {
	dir, env := zshCodeEnv(t, "/usr/bin/fish")
	for _, line := range []string{
		`sudo -s ls`,
		`tmux new-window 'ls'`,
		`flock /tmp/l -c 'ls'`,
		`script -qc 'ls' /dev/null`,
		`su -c 'ls'`, // SHELL besides root's bash
	} {
		if got := handedIn("bash", dir, env, line).Dynamic; !slices.Contains(got, dynComputed) {
			t.Errorf("SHELL=fish %q: %v, want computed", line, got)
		}
	}
	for _, line := range []string{`ssh host ls`, `vim x`, `tmux ls`, `bash -c 'ls'`} {
		if got := handedIn("bash", dir, env, line).Dynamic; len(got) > 0 {
			t.Errorf("SHELL=fish %q: %v, want none", line, got)
		}
	}
}

// TestZshGlobalAliases: zsh puts the value of a global alias in place of a
// word that names it, in the line and in the code of its eval; such a line
// is computed. A quoted or escaped word is no alias, nor one inside
// another word, nor a word of the code of a zsh the line starts or of a
// bash shell.
func TestZshGlobalAliases(t *testing.T) {
	ctx := context.Background()
	e, err := Load(ctx, t.TempDir(), Rules{Deny: []string{"rm *"}})
	if err != nil {
		t.Fatal(err)
	}
	dir, env := zshCodeEnv(t, "")
	in := func(shell, line string, aliases []string) Input {
		t.Helper()
		i := NewInputIn(shell, "bash", map[string]any{"command": line}, dir, env, zshDefaults...)
		i.GlobalAliases(aliases)
		i.HandOff(line)
		return i
	}
	aliases := []string{"G", "...", "NUL"}
	for _, line := range []string{
		`echo 'rm -rf x' G`,
		`echo x G | cat`,
		`G`,
		`cat x > NUL`,
		`cd ...`,
		`for f in G; do echo $f; done`,
		`eval 'echo x G'`,
		`echo $(echo x G)`,
		`case x in G) ;; esac`,
		`[[ x == G ]]`,
		`a=( x G )`,
		`export G`,
	} {
		i := in("zsh", line, aliases)
		if !slices.Contains(i.Dynamic, dynComputed) {
			t.Errorf("%q: %v, want computed", line, i.Dynamic)
		}
		if d, _ := e.Check(ctx, i); d.Action == Allow {
			t.Errorf("%q: allow", line)
		}
	}
	for _, line := range []string{
		`echo 'rm -rf x G'`,
		`echo "G"`,
		`echo \G`,
		`echo xG Gx`,
		`x=G`,
		`echo ${x:-G}`,
		`zsh -c 'echo x G'`,
		`echo x`,
	} {
		if got := in("zsh", line, aliases).Dynamic; slices.Contains(got, dynComputed) {
			t.Errorf("%q: %v, want no computed", line, got)
		}
	}
	if got := in("zsh", `echo 'rm -rf x' G`, nil).Dynamic; len(got) > 0 {
		t.Errorf("no aliases: %v, want none", got)
	}
	if got := in("bash", `echo 'rm -rf x' G`, aliases).Dynamic; len(got) > 0 {
		t.Errorf("bash: %v, want none", got)
	}
}
