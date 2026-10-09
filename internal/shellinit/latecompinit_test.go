package shellinit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// lateCompinit is a precmd hook of the user's that loads compinit at the
// first prompt, as zsh4humans does, then what follows it in the hook.
func lateCompinit(then string) string {
	return "__u_late() {\n\t[[ -n ${__u_done-} ]] && return 0\n\ttypeset -g __u_done=1\n" +
		"\tautoload -Uz compinit && compinit -u -D\n" + then + "}\nprecmd_functions+=(__u_late)\n"
}

// zshTabLate is zshTab for a zshrc without compinit: the lines of before
// run first, a prompt each, and the cases are typed at the prompt after
// them. The test is skipped if compinit was loaded before the zshrc, by
// the zshrc of the system.
func zshTabLate(t *testing.T, zshrc string, before []string, cases []tabCase, after string) ([]string, string) {
	t.Helper()
	var steps []zstep
	for i, b := range before {
		steps = append(steps, zstep{prompts: i + 1, keys: b + "\r"})
	}
	steps = append(steps, zstep{prompts: len(before) + 1})
	for _, c := range cases {
		steps = append(steps, zstep{keys: c.keys + "\x18d"})
	}
	steps = append(steps, zstep{keys: after + ": >../done\r"}, zstep{file: "done"})
	dir, _ := zshTyped(t, "print -r -- ${+functions[compdef]} >$HOME/global\n"+zshrc, steps...)
	if b, _ := os.ReadFile(filepath.Join(dir, "global")); string(b) != "0\n" {
		t.Skip("the zshrc of the system loads compinit")
	}
	lines, _ := os.ReadFile(filepath.Join(dir, "line"))
	got := strings.Split(string(lines), "\n")
	for i, c := range cases {
		if compAt(got, i) != c.want {
			t.Errorf("%q: %q, want %q", c.keys, compAt(got, i), c.want)
		}
	}
	return got, dir
}

// TestCompleteZshLate: a compinit later than the .zshrc, from a precmd
// hook or a line run at a prompt as zinit's turbo does, sets Tab up from
// the prompt after it, once. The user's -first- and completion of aish
// stay, and a compdef that is not compinit's, as zsh4humans has before its
// compinit, is not called.
func TestCompleteZshLate(t *testing.T) {
	zshPath(t)
	t.Run("precmd", func(t *testing.T) {
		cases := append([]tabCase{
			{"?@int\t", "?@internal/"},
			{"compfnxyz @int\t", "compfnxyz @int"},
		}, tabCases...)
		_, dir := zshTabLate(t, zshComp(false)+lateCompinit(""), nil, cases, "")
		asked, _ := os.ReadFile(filepath.Join(dir, "asked"))
		if want := "__complete aish res\n__complete aish my\n"; string(asked) != want {
			t.Errorf("asked aish %q, want %q", asked, want)
		}
	})
	t.Run("typed", func(t *testing.T) {
		wait := `print -r -- "[${__aish_comp_wait-unset}]" >>$HOME/wait`
		_, dir := zshTabLate(t, zshComp(false), []string{wait, "autoload -Uz compinit && compinit -u -D"},
			[]tabCase{
				{"Explain the @int\t", "Explain the @internal/"},
				{"/fi\t", "/fix-issue "},
				{"aish res\t", "aish resume "},
			}, wait+"\r")
		if b, _ := os.ReadFile(filepath.Join(dir, "wait")); string(b) != "[1]\n[]\n" {
			t.Errorf("waiting for compinit: %q, want %q", b, "[1]\n[]\n")
		}
	})
	t.Run("user's -first- and aish", func(t *testing.T) {
		rc := zshComp(false) + "__u_first() { [[ $PREFIX == zz* ]] && compadd zzuser && _compskip=all }\n" +
			"__u_aish() { compadd usera }\n" +
			lateCompinit("\tcompdef __u_first -first-\n\tcompdef __u_aish aish\n")
		zshTabLate(t, rc, nil, []tabCase{
			{"Explain zz\t", "Explain zzuser "},
			{"Explain @int\t", "Explain @internal/"},
			{"aish u\t", "aish usera "},
		}, "")
	})
	t.Run("compdef before compinit", func(t *testing.T) {
		rc := zshComp(false) + "compdef() { print -r -- \"$*\" >>$HOME/early }\n" +
			"__u_late() {\n\tprecmd_functions=(${precmd_functions:#__u_late})\n\tunfunction compdef\n" +
			"\tautoload -Uz compinit && compinit -u -D\n}\nprecmd_functions+=(__u_late)\n"
		_, dir := zshTabLate(t, rc, nil, []tabCase{
			{"Explain @int\t", "Explain @internal/"},
			{"aish res\t", "aish resume "},
		}, "")
		if b, err := os.ReadFile(filepath.Join(dir, "early")); err == nil {
			t.Errorf("the compdef before compinit was called: %q", b)
		}
	})
	t.Run("user's options", func(t *testing.T) {
		rc := zshComp(false) + lateCompinit("") +
			"setopt ksharrays shwordsplit nounset warncreateglobal warnnestedvar globsubst " +
			"rcquotes kshglob noglob errreturn errexit\nalias compadd='echo ALIAS' local='echo LOCAL'\n"
		zshTabLate(t, rc, []string{": one"}, []tabCase{
			{"Explain @int\t", "Explain @internal/"},
			{"/fi\t", "/fix-issue "},
			{"aish res\t", "aish resume "},
		}, "")
	})
}
