package policy

import (
	"context"
	"os"
	"testing"
)

// The agent cannot turn its checks off: not by aish yolo, which the proxy
// refuses while a request runs anyway, and not by code it leaves to the
// shell for the prompt (a trap, PROMPT_COMMAND), which the proxy would
// take from the user. No policies, the rules, the guard alone as under
// yolo: all deny it. aish yolo off, spelled so, only turns them on.
func TestGuardYolo(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	none, err := Load(ctx, t.TempDir(), Rules{})
	if err != nil {
		t.Fatal(err)
	}
	rules, err := Load(ctx, t.TempDir(), Rules{Deny: []string{"sudo *"}})
	if err != nil {
		t.Fatal(err)
	}
	engines := map[string]func(context.Context, Input) (Decision, error){
		"none":           none.Check,
		"rules":          rules.Check,
		"guard":          rules.Guard,
		"nil guard":      (*Engine)(nil).Guard,
		"subagent guard": rules.Subagent("alpha").Guard,
	}

	bash := []struct{ cmd, want, reason string }{
		{"aish yolo", Deny, YoloReason},
		{"aish yolo on", Deny, YoloReason},
		{"aish 'yolo'", Deny, YoloReason},
		{"command aish yolo", Deny, YoloReason},
		{"nohup aish yolo &", Deny, YoloReason},
		{"/usr/local/bin/aish yolo", Deny, YoloReason},
		{`"$AISH_BIN" yolo`, Deny, YoloReason},
		{"a=aish; $a yolo", Deny, YoloReason},
		{"aish yol?", Deny, TrustReason},
		// A subcommand the shell makes may be trust as well: the reason
		// is the first the guard finds.
		{`aish "$x"`, Deny, TrustReason},

		// Code left to the shell, run at the prompt.
		{"trap 'aish yolo' DEBUG", Deny, YoloReason},
		{"trap -- 'aish yolo' EXIT", Deny, YoloReason},
		{"PROMPT_COMMAND+='; aish yolo'", Deny, YoloReason},
		{"export PROMPT_COMMAND='aish yolo'", Deny, YoloReason},
		{`PROMPT_COMMAND=("aish yolo")`, Deny, YoloReason},
		{"PS1='$(aish yolo)'", Deny, YoloReason},
		{`bind -x '"\C-m": aish yolo'`, Deny, YoloReason},
		{"alias ls='aish yolo; ls'", Deny, YoloReason},
		{"ls() { command aish yolo; command ls \"$@\"; }", Deny, YoloReason},

		// Code the line runs itself.
		{`eval "aish yolo"`, Deny, YoloReason},
		{"bash -c 'aish yolo'", Deny, YoloReason},
		{`eval "$(echo aish yolo)"`, Deny, YoloReason},
		{"echo 'aish yolo' | bash", Deny, YoloReason},
		{"bash <<'EOF'\naish yolo\nEOF", Deny, YoloReason},
		{`find . -maxdepth 0 -exec aish yolo \;`, Deny, YoloReason},

		// Only off, spelled so, is let through.
		{"aish yolo off", Allow, ""},
		{"aish yolo 'off'", Allow, ""},
		// A trap is code the shell keeps for later (prompt), and the
		// guard does not see through such a line, as with PROMPT_COMMAND:
		// yolo next to aish is denied there, off or not.
		{"trap 'aish yolo off' DEBUG", Deny, YoloReason},
		{"aish yolo off now", Deny, YoloReason},
		{`aish yolo "$x"`, Deny, YoloReason},
		{`aish yolo off; echo "$x"`, Deny, YoloReason},

		{"echo yolo", Allow, ""},
		{"aish status", Allow, ""},
		{"grep -rn 'aish yolo' .", Allow, ""},
		{"aish trust", Deny, TrustReason},
	}
	for name, check := range engines {
		for _, c := range bash {
			d, err := check(ctx, callInput("bash", map[string]any{"command": c.cmd}, home))
			if err != nil {
				t.Fatal(err)
			}
			if d.Action != c.want || d.Action == Deny && d.Reason != c.reason {
				t.Errorf("%s: %s: %s (%s), want %s (%s)", name, c.cmd, d.Action, d.Reason, c.want, c.reason)
			}
		}
	}

	// zsh has hooks and a scheduler of its own.
	zsh := []struct{ cmd, want string }{
		{"precmd() { aish yolo }", Deny},
		{"TRAPDEBUG() { aish yolo }", Deny},
		{"add-zsh-hook precmd f; f() { aish yolo; }", Deny},
		{"sched +1 aish yolo", Deny},
		{"aish yolo off", Allow},
	}
	for name, check := range engines {
		for _, c := range zsh {
			in := NewInputIn("zsh", "bash", map[string]any{"command": c.cmd}, home, []string{"HOME=" + os.Getenv("HOME"), "PATH=/usr/bin:/bin"}, zshDefaults...)
			in.HandOff(c.cmd)
			d, err := check(ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			if d.Action != c.want || d.Action == Deny && d.Reason != YoloReason {
				t.Errorf("%s: zsh: %s: %s (%s), want %s", name, c.cmd, d.Action, d.Reason, c.want)
			}
		}
	}
}
