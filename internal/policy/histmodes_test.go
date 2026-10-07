package policy

import (
	"context"
	"testing"
)

// checkModes checks each line with deny = ["sudo *"], as handed to a shell
// with env and the options opts on.
func checkModes(t *testing.T, cases []modeCase) {
	t.Helper()
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	e, err := Load(ctx, t.TempDir(), Rules{Deny: []string{"sudo *"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		in := NewInput("bash", map[string]any{"command": c.cmd}, home, append([]string{"HOME=" + home}, c.env...), c.opts...)
		in.HandOff(c.cmd)
		d, err := e.Check(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		if d.Action != c.want || d.Reason != c.reason {
			t.Errorf("%q in %q %q: %s (%s), want %s (%s)", c.cmd, c.opts, c.env, d.Action, d.Reason, c.want, c.reason)
		}
	}
}

type modeCase struct {
	cmd          string
	env, opts    []string
	want, reason string
}

const computedReason = "command built at run time (computed)"

// Under set -o history and set -H, bash expands !!:s/x/s/ in a line it
// reads after them into the line before with x made s: xudo ls runs as
// sudo ls. The code a shell may read so is computed: in bash -c and eval
// after set -o history, in the line itself (eval in the user's shell, with
// histexpand on), in a shell reading stdin started with -i, the options or
// SHELLOPTS listing them, the line's own or exported. Without the mode a !
// is as before: eval and bash -c read their code with history off, and the
// user's shell has it on only for what is typed.
func TestRulesHistoryModes(t *testing.T) {
	hist := "\nxudo ls\n!!:s/x/s/"
	interactive := []string{"braceexpand", "emacs", "hashall", "histexpand", "history", "interactive-comments", "interactive_comments", "monitor"}
	exported := []string{"SHELLOPTS=braceexpand:emacs:hashall:histexpand:history:interactive-comments:monitor"}
	checkModes(t, []modeCase{
		{cmd: "bash -c $'set -o history -H" + `\nxudo ls\n!!:s/x/s/'`, want: Ask, reason: computedReason},
		{cmd: "bash -c 'set -o history -H" + hist + "'", want: Ask, reason: computedReason},
		{cmd: "bash -c 'shopt -so history histexpand" + hist + "'", want: Ask, reason: computedReason},
		{cmd: "bash -c 'set -H" + hist + "'", want: Ask, reason: computedReason},
		{cmd: "eval 'set -o history" + hist + "'", opts: interactive, want: Ask, reason: computedReason},
		{cmd: "set -o history" + hist, opts: interactive, want: Ask, reason: computedReason},
		{cmd: "set -o history" + hist + "\nset +o history", opts: interactive, want: Ask, reason: computedReason},
		{cmd: "bash -i <<< '" + hist + "'", want: Ask, reason: computedReason},
		{cmd: "bash -o history -H -s <<< '" + hist + "'", want: Ask, reason: computedReason},
		{cmd: "env SHELLOPTS=history:histexpand bash <<< '" + hist + "'", want: Ask, reason: computedReason},
		{cmd: "bash <<< '" + hist + "'", env: exported, want: Ask, reason: computedReason},
		{cmd: "bash -s <<'E'" + hist + "\nE", env: exported, want: Ask, reason: computedReason},
		// Either option may find the other on.
		{cmd: "bash -H <<< 'echo hi'", want: Ask, reason: computedReason},

		// No mode: as before.
		{cmd: "echo !", want: Allow},
		{cmd: "echo !!", opts: interactive, want: Allow},
		{cmd: "eval 'echo !!'", opts: interactive, want: Allow},
		{cmd: "bash -c 'xudo ls" + hist + "'", want: Allow},
		{cmd: "bash -ic 'xudo ls" + hist + "'", want: Allow},
		{cmd: "bash -c 'echo !x'", env: exported, opts: interactive, want: Allow},
		{cmd: "bash <<< '" + hist + "'", want: Allow},
		{cmd: "find . -exec bash -c 'echo !x' \\;", env: exported, want: Allow},
		{cmd: "set +H; echo !!", opts: interactive, want: Allow},
	})
}

// With interactive_comments off an interactive shell, the user's and its
// eval, reads # as any other character: echo A # ; sudo ls runs sudo.
// Code read so that has a comment is computed: in the line of a shell
// whose options have it off, in eval after shopt -u interactive_comments,
// in bash +O interactive_comments. A # that starts no comment, and a shell
// with comments on, are as before.
func TestRulesCommentModes(t *testing.T) {
	on := []string{"braceexpand", "hashall", "interactive-comments", "interactive_comments"}
	off := []string{"braceexpand", "hashall"}
	checkModes(t, []modeCase{
		{cmd: `shopt -u interactive_comments; eval 'echo A # ; sudo ls'`, want: Ask, reason: computedReason},
		{cmd: `shopt -u interactive_comments; eval 'echo A # ; sudo ls'; shopt -s interactive_comments`, want: Ask, reason: computedReason},
		{cmd: `set +o interactive-comments; eval 'echo A # ; sudo ls'; set -o interactive-comments`, want: Ask, reason: computedReason},
		{cmd: `shopt -uo interactive-comments; eval 'echo A # ; sudo ls'; shopt -so interactive-comments`, want: Ask, reason: computedReason},
		{cmd: `shopt -u "$o"; eval 'echo A # ; sudo ls'; shopt -s interactive_comments`, want: Ask, reason: computedReason},
		{cmd: `shopt -u interactive_comments`, opts: on, want: Ask, reason: computedReason},
		{cmd: `echo A # ; sudo ls`, opts: off, want: Ask, reason: computedReason},
		{cmd: "echo A\n# ; sudo ls", opts: off, want: Ask, reason: computedReason},
		{cmd: `shopt -s interactive_comments; echo A # ; sudo ls`, opts: off, want: Ask, reason: computedReason},
		{cmd: `echo A # ; sudo ls`, env: []string{"BASHOPTS=checkwinsize:extglob"}, want: Ask, reason: computedReason},
		{cmd: `bash -i +O interactive_comments -c 'echo A # ; sudo ls'`, want: Ask, reason: computedReason},
		{cmd: `bash -i +o interactive-comments -c 'echo A # ; sudo ls'`, want: Ask, reason: computedReason},

		// No # starts a comment, or comments are on: as before.
		{cmd: `echo hi # comment`, want: Allow},
		{cmd: `echo hi # comment`, opts: on, want: Allow},
		{cmd: `echo A # ; sudo ls`, opts: on, env: []string{"BASHOPTS=extglob:interactive_comments"}, want: Allow},
		{cmd: `echo a#b ${#PATH} $#`, opts: off, want: Allow},
		{cmd: `bash -c 'echo A # ; sudo ls'`, want: Allow},
		{cmd: `find . -exec sh -c 'echo A # ; sudo ls' sh {} \;`, want: Allow},
		{cmd: `shopt -u interactive_comments; echo hi; shopt -s interactive_comments`, want: Allow},
		{cmd: `bash -i -O interactive_comments -c 'echo A # ; sudo ls'`, want: Allow},
	})
}

// bash translates $"…" by the .mo file of TEXTDOMAIN under TEXTDOMAINDIR
// and expands the translation: its $(…) runs. Whatever the line or the
// shell holds of them, a $"…" is computed; an assignment to either is
// rebind, as one to the other variables that have programs load code.
func TestRulesTranslations(t *testing.T) {
	checkModes(t, []modeCase{
		{cmd: `echo $"hello"`, want: Ask, reason: computedReason},
		{cmd: `bash -c 'echo $"hello"'`, want: Ask, reason: computedReason},
		{cmd: `TEXTDOMAINDIR=/tmp/t TEXTDOMAIN=t bash -c 'echo hi'`, want: Ask, reason: "command built at run time (rebind)"},
		{cmd: `export TEXTDOMAINDIR=/tmp/t`, want: Ask, reason: "command built at run time (rebind)"},
		{cmd: `TEXTDOMAIN=t; eval 'echo $"hello"'`, want: Ask, reason: "command built at run time (computed, rebind)"},
		{cmd: `echo "hello" $'hi' '$"hi"'`, want: Allow},
	})
}
