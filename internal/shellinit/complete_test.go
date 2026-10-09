package shellinit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// compHome is what the completion tests' rc files make in the home: files
// to attach in proj, where the shell runs, a skill of ~/.claude, one of
// $XDG_CONFIG_HOME/aish and one of proj/.claude, and an aish that answers
// __complete with a few words.
const compHome = `mkdir -p proj/internal/x proj/.hid .claude/skills/fix-issue xdg/aish/skills/deploy .claude/skills/bad.name
: >proj/internal/y.go; : >'proj/internal/a b'; : >proj/main.go
: >.claude/skills/fix-issue/SKILL.md; : >xdg/aish/skills/deploy/SKILL.md; : >.claude/skills/bad.name/SKILL.md
mkdir -p proj/.claude/skills/release; : >proj/.claude/skills/release/SKILL.md
printf '#!/bin/sh\nprintf "%%s\\n" "$*" >>"$HOME/asked"\nprintf "resume\\nrecap\\nsession\\nmy session\\n"\n' >stub
chmod +x stub
cd proj
`

// tabCase is a line typed with Tab in it and the line Tab left.
type tabCase struct{ keys, want string }

// tabCases are those both shells complete alike.
var tabCases = []tabCase{
	{"Explain the @int\t", "Explain the @internal/"},
	{"Explain the @internal/y\t", "Explain the @internal/y.go "},
	{"Explain @internal/a\t", "Explain @internal/a"}, // a name with a blank is no @path
	{"Explain @.h\t", "Explain @.hid/"},
	{"Explain @ma\t", "Explain @main.go "},
	{"@int\t", "@internal/"},
	{"Explain the inter\t", "Explain the internal/"}, // a word of a request, as before
	{"/fi\t", "/fix-issue "},
	{"/de\t", "/deploy "},
	{"/rel\t", "/release "},
	{"/bad\t", "/bad"}, // no skill name, no path
	{"/us\t", "/usr/"}, // no skill: a path
	{"aish res\t", "aish resume "},
	{"aish my\t", `aish my\ session `},
	{"compfnxyz a@int\t", "compfnxyz a@int"},
}

// TestCompleteBash types Tab in a bash with the rc aish gives it: @path in
// a request and /skill, the words of aish from aish __complete, and what
// bash completed before, command names and words of commands with or
// without the user's complete -D. A -D or -I of the user's, from
// bash-completion too, goes on working, and so does his compspec for
// aish.
func TestCompleteBash(t *testing.T) {
	common := compHome + "AISH_BIN=$HOME/stub\ncompfnxyz() { :; }\n" +
		`bind -x '"\C-xd": printf "%s\n" "$READLINE_LINE" >>"$HOME/line"; READLINE_LINE='` + "\n"
	cases := append([]tabCase{
		{"compfnx\t", "compfnxyz "},
		{"compfnxyz @int\t", "compfnxyz @int"}, // a command's @ word is bash's: a host name
	}, tabCases...)
	for _, tc := range []struct {
		name, bashrc string
		cases        []tabCase
		specs        string // complete -p -D, -I and aish after it all
	}{
		{
			name:  "no specs of the user's",
			cases: cases,
			specs: "complete -F __aish_comp_D -D\ncomplete -F __aish_comp_I -I\ncomplete -F __aish_comp_aish aish\n",
		},
		{
			name: "user's -D, -I and aish",
			bashrc: "__u_d() { COMPREPLY=(userd); }; complete -o nospace -D -F __u_d\n" +
				"__u_i() { COMPREPLY=(useri); }; complete -I -F __u_i\n" +
				"__u_a() { COMPREPLY=(usera); }; complete -F __u_a aish\n",
			cases: []tabCase{
				{"compfnxyz a\t", "compfnxyz userd"},
				{"Explain zz\t", "Explain zz"}, // a request: not his
				{"Explain @int\t", "Explain @internal/"},
				{"compf\t", "useri "},
				{"@int\t", "@internal/"},
				{"/fi\t", "/fix-issue "},
				{"aish x\t", "aish usera "},
			},
			specs: "complete -o nospace -F __aish_comp_D -D\ncomplete -F __aish_comp_I -I\ncomplete -F __u_a aish\n",
		},
		{
			// A function that failed or read an unset variable would
			// close the shell; globs are the subshell's own.
			name:   "user's set -euk, noglob and failglob",
			bashrc: "set -euk -f\nshopt -s failglob nocaseglob\n",
			cases: []tabCase{
				{"Explain the @int\t", "Explain the @internal/"},
				{"Explain the inter\t", "Explain the internal/"},
				{"/fi\t", "/fix-issue "},
				{"/us\t", "/usr/"},
				{"compfnx\t", "compfnxyz "},
				{"aish res\t", "aish resume "},
			},
			specs: "complete -F __aish_comp_D -D\ncomplete -F __aish_comp_I -I\ncomplete -F __aish_comp_aish aish\n",
		},
		{
			name:   "user's -D of no function",
			bashrc: "complete -D -W wordd\n",
			cases: []tabCase{
				{"Explain @int\t", "Explain @int"},
				{"Explain w\t", "Explain wordd "},
				{"@int\t", "@internal/"},
			},
			specs: "complete -W 'wordd' -D\ncomplete -F __aish_comp_I -I\ncomplete -F __aish_comp_aish aish\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			keys := []string{": >../ready\r"}
			for _, c := range tc.cases {
				keys = append(keys, c.keys+"\x18d")
			}
			keys = append(keys, "{ complete -p -D; complete -p -I; complete -p aish; } >../specs 2>&1\r")
			dir, out := typed(t, "", tc.bashrc+common, keys...)
			lines, _ := os.ReadFile(filepath.Join(dir, "line"))
			got := strings.Split(string(lines), "\n")
			for i, c := range tc.cases {
				if i >= len(got) || got[i] != c.want {
					t.Errorf("%q: %q, want %q", c.keys, compAt(got, i), c.want)
				}
			}
			if specs, _ := os.ReadFile(filepath.Join(dir, "specs")); string(specs) != tc.specs {
				t.Errorf("specs:\n%s\nwant\n%s", specs, tc.specs)
			}
			if strings.Contains(out, "__aish") && strings.Contains(out, "error") {
				t.Errorf("init.bash complained:\n%s", out)
			}
		})
	}
}

// TestCompleteBashCompletion: with bash-completion loaded by ~/.bashrc,
// its loader is the user's -D: a command's words come from the compspec
// it loads, and a Tab in a request, which goes past it, leaves none behind
// for the first word, which would know no @.
func TestCompleteBashCompletion(t *testing.T) {
	const bc = "/usr/share/bash-completion/bash_completion"
	if _, err := os.Stat(bc); err != nil {
		t.Skip("no bash-completion")
	}
	rc := "source " + bc + "\n" + compHome + "compfnxyz() { :; }\n" +
		`bind -x '"\C-xd": printf "%s\n" "$READLINE_LINE" >>"$HOME/line"; READLINE_LINE='` + "\n"
	cases := []tabCase{
		{"Explain the inter\t", "Explain the internal/"},
		{"Explain the @int\t", "Explain the @internal/"},
	}
	keys := []string{": >../ready\r"}
	for _, c := range cases {
		keys = append(keys, c.keys+"\x18d")
	}
	// One signal only: a list of them would ask whether to show it.
	keys = append(keys, "kill -SIGKIL\t\x18d", "{ complete -p -D; complete -p Explain; complete -p kill; printf '%s\\n' \"$__aish_comp_dprev\"; } >../specs 2>&1\r")
	dir, _ := typed(t, "", rc, keys...)
	lines, _ := os.ReadFile(filepath.Join(dir, "line"))
	got := strings.Split(string(lines), "\n")
	for i, c := range cases {
		if compAt(got, i) != c.want {
			t.Errorf("%q: %q, want %q", c.keys, compAt(got, i), c.want)
		}
	}
	specs, _ := os.ReadFile(filepath.Join(dir, "specs"))
	if s := string(specs); !strings.HasPrefix(s, "complete -F __aish_comp_D -D\n") ||
		!strings.Contains(s, "Explain: no completion specification") || strings.Contains(s, "kill: no completion") ||
		!strings.HasSuffix(s, "\n_comp_complete_load\n") && !strings.HasSuffix(s, "\n_completion_loader\n") {
		t.Errorf("specs:\n%s", s)
	}
}

// zshComp is what a .zshrc for the zsh tests of Tab has: compinit, unless
// told not to, the home of compHome and a key that writes the line to the
// file line and empties it.
func zshComp(compinit bool) string {
	rc := ""
	if compinit {
		rc = "autoload -Uz compinit && compinit -u -D\n"
	}
	return rc + compHome + "AISH_BIN=$HOME/stub\ncompfnxyz() { :; }\n" +
		"__t_dump() { print -r -- \"$BUFFER\" >>$HOME/line; BUFFER= }\nzle -N __t_dump\nbindkey '^Xd' __t_dump\n"
}

// zshTab types the cases at the prompt of a zsh with zshrc and returns the
// lines Tab left and the home.
func zshTab(t *testing.T, zshrc string, cases []tabCase, after string) ([]string, string) {
	t.Helper()
	steps := []zstep{{prompts: 1}}
	for _, c := range cases {
		steps = append(steps, zstep{keys: c.keys + "\x18d"})
	}
	steps = append(steps, zstep{keys: after + ": >../done\r"}, zstep{file: "done"})
	dir, _ := zshTyped(t, zshrc, steps...)
	lines, _ := os.ReadFile(filepath.Join(dir, "line"))
	got := strings.Split(string(lines), "\n")
	for i, c := range cases {
		if compAt(got, i) != c.want {
			t.Errorf("%q: %q, want %q", c.keys, compAt(got, i), c.want)
		}
	}
	return got, dir
}

// TestCompleteZsh types Tab in a zsh with compinit: as in bash, @path,
// /skill and the words of aish come on top of what zsh completed. The
// user's -first- goes on for other words, and his completion of aish
// stays. Without compinit nothing is set, and Tab is as it was.
func TestCompleteZsh(t *testing.T) {
	zshPath(t)
	t.Run("compinit", func(t *testing.T) {
		cases := append([]tabCase{
			{"?@int\t", "?@internal/"}, // bash has a host name there
			{"compfnx\t", "compfnxyz "},
			{"compfnxyz @int\t", "compfnxyz @int"}, // a command's @ word is zsh's
		}, tabCases...)
		_, dir := zshTab(t, zshComp(true), cases, "")
		asked, _ := os.ReadFile(filepath.Join(dir, "asked"))
		if want := "__complete aish res\n__complete aish my\n"; string(asked) != want {
			t.Errorf("asked aish %q, want %q", asked, want)
		}
	})
	t.Run("user's -first- and aish", func(t *testing.T) {
		rc := zshComp(true) + "__u_first() { [[ $PREFIX == zz* ]] && compadd zzuser && _compskip=all }\ncompdef __u_first -first-\n" +
			"__u_aish() { compadd usera }\ncompdef __u_aish aish\n"
		zshTab(t, rc, []tabCase{
			{"Explain zz\t", "Explain zzuser "},
			{"Explain @int\t", "Explain @internal/"},
			{"aish u\t", "aish usera "},
		}, "")
	})
	// Not shglob: compsys itself does not complete under it.
	t.Run("user's options", func(t *testing.T) {
		rc := zshComp(true) + "setopt ksharrays shwordsplit nounset warncreateglobal warnnestedvar globsubst " +
			"rcquotes kshglob noglob errreturn\nalias compadd='echo ALIAS' local='echo LOCAL'\n"
		zshTab(t, rc, []tabCase{
			{"Explain @int\t", "Explain @internal/"},
			{"/fi\t", "/fix-issue "},
			{"aish res\t", "aish resume "},
		}, "")
	})
	t.Run("no compinit", func(t *testing.T) {
		rc := "print -r -- ${+functions[compdef]} >$HOME/global\n" + zshComp(false)
		got, dir := zshTab(t, rc, []tabCase{{": inter\t", ": internal/"}},
			"print -r -- ${+functions[compdef]} ${+__aish_comp_prev} >>$HOME/line\r")
		if b, _ := os.ReadFile(filepath.Join(dir, "global")); string(b) != "0\n" {
			t.Skip("the zshrc of the system loads compinit")
		}
		if compAt(got, 1) != "0 0" {
			t.Errorf("without compinit: %q", got)
		}
	})
}

// TestCompleteZshScript: what `aish completion zsh` prints completes aish
// as a file of $fpath compinit finds and as a script sourced after it,
// and init.zsh leaves it so.
func TestCompleteZshScript(t *testing.T) {
	zshPath(t)
	script := "cat >$HOME/_aish <<'__EOF'\n" + CompleteZsh + "__EOF\n"
	for name, rc := range map[string]string{
		"fpath":  script + "fpath=($HOME $fpath)\n" + zshComp(true),
		"source": zshComp(true) + "(cd && " + script + ")\nsource $HOME/_aish\n",
	} {
		t.Run(name, func(t *testing.T) {
			got, _ := zshTab(t, rc, []tabCase{{"aish res\t", "aish resume "}},
				"print -r -- $_comps[aish] >>$HOME/line\r")
			if compAt(got, 1) != "_aish" {
				t.Errorf("aish completed by %q, want _aish", compAt(got, 1))
			}
		})
	}
}

func compAt(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return "<none>"
}
