package proxy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/session"
	"github.com/GoldenDeals/aish/internal/shells"
	"github.com/GoldenDeals/aish/internal/shellstate"
)

// A session saved in bash, resumed in a zsh: its variables are bash code,
// which zsh would not read as such; the zsh goes back to how it started,
// then to the session's directory. The options the policy reads the
// agent's commands in are zsh's, by zsh's names.
func TestResumeBashInZsh(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	other, err := session.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	other.Append(session.Entry{Kind: session.KindUser, Text: "hi"})
	if err := other.Save(); err != nil {
		t.Fatal(err)
	}
	saved := session.Saved{Shell: shellstate.State{Vars: map[string]string{"B": `declare -- B="1"`}, Cwd: "/srv"}}
	if err := session.SaveState(dir, other.ID, saved); err != nil {
		t.Fatal(err)
	}
	other.Unlock()

	sess, err := session.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := New(sess)
	p.shell = shells.Zsh{}
	p.run = t.TempDir()
	p.base = &shellstate.State{Kind: shellstate.Zsh, Vars: map[string]string{}, Opts: map[string]string{"nomatch": "setopt nomatch", "autocd": "unsetopt autocd"}}
	p.cur = &shellstate.State{Kind: shellstate.Zsh, Vars: map[string]string{"Z": "unset -v Z 2>/dev/null; typeset -g Z=1"},
		Opts: map[string]string{"nomatch": "setopt nomatch", "autocd": "setopt autocd"}, Cwd: "/home"}
	if on := p.shellOpts(); len(on) != 2 {
		t.Errorf("options on %q", on)
	}
	b, _ := json.Marshal(rpc.ResumeParams{ID: other.ID})
	if _, err := p.handle(asUser(p), rpc.MethodResume, b); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(p.run, "restore.bash"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(got)
	for _, want := range []string{"unset -v Z", "\\unsetopt 'autocd'", "builtin cd -- '/srv'", "export 'AISH_SESSION=" + other.ID + "'"} {
		if !strings.Contains(script, want) {
			t.Errorf("restore.bash lacks %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "declare") || strings.Contains(script, "B=") {
		t.Errorf("restore.bash has bash's code:\n%s", script)
	}
}
