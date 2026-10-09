package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/GoldenDeals/aish/internal/policy"
	"github.com/GoldenDeals/aish/internal/rpc"
)

// withTerminal gives p a terminal to ask on.
func withTerminal(p *Proxy) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.size == nil {
		p.size = func() (int, int) { return 200, 24 }
	}
}

type appliedResult struct {
	res rpc.Applied
	err error
}

// applyCall calls rpc apply_config with ap and ctx, as the shell's
// foreground job at a terminal; the call's result comes on the channel.
func applyCall(p *Proxy, ctx context.Context, ap rpc.AgentParams) <-chan appliedResult {
	withTerminal(p)
	res := make(chan appliedResult, 1)
	go func() {
		b, _ := json.Marshal(ap)
		v, err := p.handle(rpc.WithPeer(ctx, os.Getpid()), rpc.MethodApplyConfig, b)
		a, _ := v.(rpc.Applied)
		res <- appliedResult{a, err}
	}()
	return res
}

// appliedNow is the result of the call applyCall made, once it returns.
func appliedNow(t *testing.T, res <-chan appliedResult) (rpc.Applied, error) {
	t.Helper()
	select {
	case r := <-res:
		return r.res, r.err
	case <-time.After(5 * time.Second):
		t.Fatal("rpc apply_config never returned")
		return rpc.Applied{}, nil
	}
}

// applyAnswered is rpc apply_config with ap, its question answered with
// key after a pause (askguard.go). A call that ends before it asks, on a
// broken file say, returns as it does.
func applyAnswered(t *testing.T, p *Proxy, ap rpc.AgentParams, key string) (rpc.Applied, error) {
	t.Helper()
	firmNow(t)
	res := applyCall(p, context.Background(), ap)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		select {
		case r := <-res:
			return r.res, r.err
		default:
		}
		p.mu.Lock()
		open := p.ask != nil
		p.mu.Unlock()
		if open {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("rpc apply_config neither asked nor returned")
		}
	}
	pause(p)
	p.key([]byte(key))
	return appliedNow(t, res)
}

// workParams are of the shell at its prompt in its work directory.
func workParams(p *Proxy) rpc.AgentParams {
	cwd := filepath.Join(os.Getenv("HOME"), "work")
	p.marker(Marker{Kind: "cmd-end", Payload: "0;" + cwd})
	return rpc.AgentParams{Cwd: cwd}
}

const (
	confirmBefore = "fold_lines = 3\nmodel = \"m\"\n\n[policy]\ndeny = [\"rm *\"]\n"
	confirmAfter  = "fold_lines = 4\nmodel = \"m2\"\n\n[policy]\ndeny = [\"ls *\"]\n"
)

// unapplied checks that p goes by confirmBefore still: the snapshot, the
// fields of the proxy, the shell's model and the policies of a request.
func unapplied(t *testing.T, p *Proxy, name string) {
	t.Helper()
	p.mu.Lock()
	cfg, _ := p.snapshot().LoadProfile("")
	fold, model := p.foldLines, p.model
	p.mu.Unlock()
	if cfg.FoldLines != 3 || fold != 3 || model != "m" {
		t.Errorf("%s: applied: fold_lines %d %d, model %s", name, cfg.FoldLines, fold, model)
	}
	ask(t, p, nil)
	if verdict(t, p.ag, "rm x") != policy.Deny || verdict(t, p.ag, "ls x") != policy.Allow {
		t.Errorf("%s: the edited policies are in force", name)
	}
}

// aish apply-config asks before the config read anew goes in force, No
// chosen at first: y, or Yes chosen with Enter, applies it; n, Enter, Esc
// or a command typed ahead leave all as it was, the call refused. No key
// goes on to the shell.
func TestApplyConfigConfirm(t *testing.T) {
	for _, tc := range []struct {
		name  string
		keys  []string // each after a pause (askguard.go)
		apply bool
	}{
		{"y", []string{"y"}, true},
		{"left, Enter", []string{"\x1b[D", "\r"}, true},
		{"n", []string{"n"}, false},
		{"Enter", []string{"\r"}, false},
		{"Esc", []string{"\x1b"}, false},
		{"a command typed ahead", []string{"ls -la\r"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			firmNow(t)
			p := configured(t, confirmBefore)
			ask(t, p, nil)
			rewrite(t, confirmAfter)
			res := applyCall(p, context.Background(), workParams(p))
			waitOpen(t, p, func() bool { return p.ask != nil })
			out := p.out.(*terminal).String()
			if !strings.Contains(out, "config.toml: fold_lines, model, policy.deny\r\n") ||
				!strings.Contains(out, "Apply?\x1b[0m "+choices("No")) {
				t.Errorf("the question %q", out)
			}
			for _, k := range tc.keys {
				pause(p)
				if pass := p.key([]byte(k)); len(pass) > 0 {
					t.Errorf("to the shell %q", pass)
				}
			}
			applied, err := appliedNow(t, res)
			if p.ask != nil {
				t.Error("the question stayed open")
			}
			if !tc.apply {
				if err == nil || err.Error() != "not confirmed; nothing applied" {
					t.Errorf("declined: %v", err)
				}
				unapplied(t, p, tc.name)
				return
			}
			if err != nil || !applied.Switched || applied.Info.Model != "m2" {
				t.Fatalf("confirmed: %v, %+v", err, applied)
			}
			ask(t, p, nil)
			if p.foldLines != 4 || verdict(t, p.ag, "rm x") != policy.Allow || verdict(t, p.ag, "ls x") != policy.Deny {
				t.Errorf("confirmed, not in force: fold_lines %d", p.foldLines)
			}
		})
	}
}

// The question of aish apply-config ends with the call: Ctrl+C reaches the
// client, which gives up and closes the connection; nothing is applied.
func TestApplyConfigConfirmCancel(t *testing.T) {
	firmNow(t)
	p := configured(t, confirmBefore)
	ask(t, p, nil)
	rewrite(t, confirmAfter)
	ctx, cancel := context.WithCancel(context.Background())
	res := applyCall(p, ctx, workParams(p))
	waitOpen(t, p, func() bool { return p.ask != nil })
	if pass := p.key([]byte{0x03}); string(pass) != "\x03" {
		t.Errorf("Ctrl+C to the shell: %q", pass)
	}
	cancel()
	if _, err := appliedNow(t, res); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
	if p.ask != nil {
		t.Error("the question stayed open")
	}
	unapplied(t, p, "cancelled")
}

// Unanswered in userWait, the question is left with No and why.
func TestApplyConfigConfirmDeadline(t *testing.T) {
	old := userWait
	userWait = 30 * time.Millisecond
	t.Cleanup(func() { userWait = old })
	p := configured(t, confirmBefore)
	ask(t, p, nil)
	rewrite(t, confirmAfter)
	_, err := appliedNow(t, applyCall(p, context.Background(), workParams(p)))
	if err == nil || err.Error() != "not confirmed: no answer in 30ms; nothing applied" {
		t.Errorf("unanswered: %v", err)
	}
	if !strings.Contains(p.out.(*terminal).String(), "No (no answer in 30ms)") {
		t.Errorf("terminal %q", p.out.(*terminal).String())
	}
	unapplied(t, p, "unanswered")
}

// What a process in the shell writes to its terminal is the shell's
// output, not keys: it does not answer the question, code the assistant
// left for the shell to run at the prompt neither.
func TestApplyConfigConfirmNotFromPTY(t *testing.T) {
	firmNow(t)
	p := configured(t, confirmBefore)
	ask(t, p, nil)
	rewrite(t, confirmAfter)
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skip("no pty:", err)
	}
	t.Cleanup(func() { ptmx.Close(); tty.Close() })
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				p.output(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	res := applyCall(p, context.Background(), workParams(p))
	waitOpen(t, p, func() bool { return p.ask != nil })
	if _, err := tty.Write([]byte("y\r\n\x1b[D\ry")); err != nil {
		t.Fatal(err)
	}
	// The output waits for the answer (askhold.go).
	for deadline := time.Now().Add(5 * time.Second); !strings.HasSuffix(heldOutput(p), "\x1b[D\ry"); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the PTY's output never came: %q", heldOutput(p))
		}
	}
	select {
	case r := <-res:
		t.Fatalf("answered by the PTY: %v", r.err)
	case <-time.After(50 * time.Millisecond):
	}
	p.key([]byte("n"))
	if _, err := appliedNow(t, res); err == nil || !strings.Contains(err.Error(), "nothing applied") {
		t.Errorf("then n: %v", err)
	}
	unapplied(t, p, "the PTY")
}

// The assistant's aish apply-config, and one in the background, are
// refused before anything is asked.
func TestApplyConfigRefusedUnasked(t *testing.T) {
	p := configured(t, confirmBefore)
	rewrite(t, confirmAfter)
	withTerminal(p)
	p.marker(Marker{Kind: "ask-start"})
	ap := rpc.AgentParams{Cwd: filepath.Join(os.Getenv("HOME"), "work")}
	if _, err := call(t, p, rpc.MethodApplyConfig, ap); err != errApplyAsks {
		t.Errorf("the assistant: %v", err)
	}
	if s := p.out.(*terminal).String(); strings.Contains(s, "Apply?") || p.ask != nil {
		t.Errorf("asked: %q", s)
	}
}

// The question names what changed, and what waits for a restart; with
// nothing changed it is asked all the same.
func TestApplyQuestion(t *testing.T) {
	home, _ := os.UserHomeDir()
	q := applyQuestion(rpc.Applied{
		Keys:    []string{"fold_lines", "shell"},
		Files:   []string{filepath.Join(home, ".config", "aish", "policy"), "/srv/x/.aish.toml"},
		Restart: []string{"shell"},
	})
	want := "\x1b[0m" + bold + "aish apply-config\x1b[0m puts in force the config as it is now:\r\n" +
		"  config.toml: fold_lines, shell\r\n  ~/.config/aish/policy\r\n  /srv/x/.aish.toml\r\n" +
		"  (only a restart of aish applies shell)\r\n" + bold + "Apply?\x1b[0m"
	if q != want {
		t.Errorf("\n%q\nwant\n%q", q, want)
	}
	if q := applyQuestion(rpc.Applied{}); !strings.Contains(q, ": nothing changed since the config was read\r\n"+bold+"Apply?") {
		t.Errorf("nothing changed: %q", q)
	}

	p := configured(t, confirmBefore)
	if _, err := applyAnswered(t, p, workParams(p), "y"); err != nil {
		t.Fatal(err)
	}
	if s := p.out.(*terminal).String(); !strings.Contains(s, "nothing changed since the config was read") {
		t.Errorf("unchanged: %q", s)
	}
}
