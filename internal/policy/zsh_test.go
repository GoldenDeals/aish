package policy

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// zshDefaults are options a zsh started without rc files has on, those
// zshShell looks at among them.
var zshDefaults = []string{"equals", "glob", "nomatch", "aliases", "bareglobqual"}

// zshDynamic is Dynamic of line handed to a zsh with opts on, from dir.
func zshDynamic(t *testing.T, dir, line string, opts []string) []string {
	t.Helper()
	env := []string{"HOME=" + dir, "PWD=" + dir, "PATH=" + filepath.Join(dir, "bin")}
	in := NewInputIn("zsh", "bash", map[string]any{"command": line}, dir, env, opts...)
	in.HandOff(line)
	return in.Dynamic
}

// TestZshMisreads: a line zsh reads otherwise than bash, the policy's
// reading, is computed, and so is the code it hands to another shell; a
// line zsh reads as bash does is as bash would have it.
func TestZshMisreads(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "bin"), 0o755)
	for _, prog := range []string{"ls", "git", "make", "rm"} {
		os.WriteFile(filepath.Join(dir, "bin", prog), []byte("#!/bin/sh\n"), 0o755)
	}
	os.Mkdir(filepath.Join(dir, "proj"), 0o755)

	for _, line := range []string{
		`ls -la`,
		`git status | head -5`,
		`echo "$HOME" '$x' \$y`,
		`for f in *.go; do echo "$f"; done`,
		`x=1; echo $x ${x:-2} $(date) $((x + 1))`,
		`cd /tmp && ls`,
		`make PREFIX=/usr install`,
		`git show HEAD~1 HEAD^`, // extendedglob off
		`echo 'a''b'`,           // rc_quotes off
		`set -euo pipefail; ls`,
		`set -- a b`,
		`cat <<'EOF'
cost: 5$ ~x ^y
EOF`,
		`print -r -- done`,
		`ls proj`,
		`echo # a comment`,
		`[[ $x == a* ]] && echo yes`,
	} {
		if got := zshDynamic(t, dir, line, zshDefaults); slices.Contains(got, dynComputed) {
			t.Errorf("%q: %v, want no computed", line, got)
		}
	}

	for _, line := range []string{
		`ls *(e:'rm -rf ~':)`,
		`ls *(+f)`,
		`echo $~x $=y`,
		`echo "$+x"`,
		`=rm -rf x`,
		`ls =ls`,
		`noglob rm *`,
		`nocorrect rm x`,
		`repeat 1 rm -rf ~`,
		`setopt extendedglob; ls ^x`,
		`unsetopt nomatch`,
		`emulate sh -c 'ls'`,
		`set -o globsubst`,
		`set -G`,
		`echo x >! f`,
		`echo x >>! f`,
		`ls <1-3>x`,
		`diff =(ls) =(ls -a)`,
		`{ ls } always { echo }`,
		`echo ${(e)x}`,
		`echo $x[1]`,
		`echo "$x[$y]"`,
		`eval 'ls *(e:x:)'`,
		`bash -c 'echo $~x'`,
		`r`,
		`fc -e - ls`,
		`builtin setopt shwordsplit`,
		`command noglob ls`,
		`exec -a x zmodload zsh/system`,
		`print -z 'rm -rf ~'`,
		`print -rs -- 'rm -rf ~'`,
		`zf_rm -rf ~`,
		`options[globsubst]=on`,
		`mapfile[/etc/x]=y`,
		`integer options=1`,
		`echo ~foo/x`,
		`make PREFIX=~/x`,
		`ls ***/x`,
		`cat >&p`,
		`zle -N w`,
		`alias -g G='| sh'`,
		`autoload -U x`,
		`sched +1 rm -rf ~`,
		`foreach x (a b); echo; end`,
		`() { echo hi }`,
		`sleep 1 &|`,
		`coproc cat`,
	} {
		if got := zshDynamic(t, dir, line, zshDefaults); !slices.Contains(got, dynComputed) {
			t.Errorf("%q: %v, want computed", line, got)
		}
	}

	for _, c := range []struct{ line, kind string }{
		{`path=(/tmp $path)`, dynRebind},
		{`commands[ls]=/tmp/x`, dynRebind},
		{`fpath+=(/tmp)`, dynRebind},
		{`typeset -g NULLCMD=x`, dynRebind},
		{`RPROMPT='$(rm -rf ~)'`, dynPrompt},
		{`precmd_functions+=(f)`, dynPrompt},
		{`precmd() { rm -rf ~; }`, dynPrompt},
		{`TRAPEXIT() { rm -rf ~; }`, dynPrompt},
	} {
		if got := zshDynamic(t, dir, c.line, zshDefaults); !slices.Contains(got, c.kind) {
			t.Errorf("%q: %v, want %s", c.line, got, c.kind)
		}
	}
}

// TestZshOptions: what zsh makes of a word with its options on, and with
// options not known, any of them.
func TestZshOptions(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "bin"), 0o755)
	os.WriteFile(filepath.Join(dir, "bin", "ls"), []byte("#!/bin/sh\n"), 0o755)
	os.Mkdir(filepath.Join(dir, "proj"), 0o755)
	for _, c := range []struct {
		line string
		opts []string
		want bool
	}{
		{`git show HEAD~1`, with(zshDefaults, "extendedglob"), true},
		{`ls a#b`, with(zshDefaults, "extendedglob"), true},
		{`ls ^x`, with(zshDefaults, "extendedglob"), true},
		{`ls ~/x`, with(zshDefaults, "extendedglob"), false},
		{`echo 'a''b'`, with(zshDefaults, "rcquotes"), true},
		{`echo $'a''b'`, with(zshDefaults, "rcquotes"), false},
		{`ls $x`, with(zshDefaults, "globsubst"), true},
		{`ls "$x"`, with(zshDefaults, "globsubst"), false},
		{`rm *.bak`, with(zshDefaults, "nullglob"), true},
		{`echo {abc}`, with(zshDefaults, "braceccl"), true},
		{`proj`, with(zshDefaults, "autocd"), true},
		{`ls`, with(zshDefaults, "autocd"), false},
		{`cd /tmp`, with(zshDefaults, "autocd"), false},
		{`no-such-program`, with(zshDefaults, "autocd"), true},
		{`=ls`, []string{}, false}, // equals off
		{`make --prefix=~/x`, with(zshDefaults, "magicequalsubst"), true},
		{`make --prefix=~/x`, zshDefaults, false},
		{`integer n=$x`, zshDefaults, true},
		{`integer n=1`, zshDefaults, false},
		{`typeset -F f=$x`, zshDefaults, true},
		{`typeset -i n=1`, zshDefaults, false},
		{`git show HEAD~1`, nil, true},
		{`rm *.bak`, nil, true},
	} {
		got := slices.Contains(zshDynamic(t, dir, c.line, c.opts), dynComputed)
		if got != c.want {
			t.Errorf("%q with %v: computed %v, want %v", c.line, c.opts, got, c.want)
		}
	}
}

// TestZshModes: zsh's cdablevars is the mode bash has for cdable_vars,
// zsh has no set -k, and it reads a comment in eval whatever its
// interactivecomments.
func TestZshModes(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	e, err := Load(ctx, t.TempDir(), Rules{Deny: []string{"sudo *"}})
	if err != nil {
		t.Fatal(err)
	}
	check := func(line string, opts []string) Decision {
		t.Helper()
		in := NewInputIn("zsh", "bash", map[string]any{"command": line}, home, []string{"HOME=" + home, "PATH=/usr/bin:/bin"}, opts...)
		in.HandOff(line)
		d, err := e.Check(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	const cdable = `x=/; cd x && rm -rf ./etc`
	if d := check(cdable, with(zshDefaults, "cdablevars")); d.Action != Ask || !strings.Contains(d.Reason, dynComputed) {
		t.Errorf("cdablevars: %+v, want ask (computed)", d)
	}
	if d := check(cdable, zshDefaults); d.Action != Allow {
		t.Errorf("no cdablevars: %+v, want allow", d)
	}
	if d := check(`git fetch GIT_SSH_COMMAND="sudo ls"`, with(zshDefaults, "interactivecomments")); d.Action != Allow {
		t.Errorf("no set -k in zsh: %+v, want allow", d)
	}
	if d := check(`echo hi # ; sudo ls`, zshDefaults); d.Action != Allow {
		t.Errorf("a comment: %+v, want allow", d)
	}
	// bash's input is NewInput's.
	a := NewInputIn("bash", "bash", nil, home, nil, "keyword")
	b := NewInput("bash", nil, home, nil, "keyword")
	if a.sh.zsh != nil || a.sh.modes != b.sh.modes {
		t.Errorf("bash: %+v, want %+v", a.sh, b.sh)
	}
}

func with(base []string, o ...string) []string { return append(slices.Clone(base), o...) }
