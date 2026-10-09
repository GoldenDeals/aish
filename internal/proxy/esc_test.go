package proxy

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/rpc"
)

// foreground is a process group standing for the agent's command in the
// foreground of the shell's terminal: Esc sends it SIGINT. Until stand is
// called the foreground is the test's own, which makes the RPC calls as
// the shell's job.
type foreground struct {
	group atomic.Int64
	cmd   *exec.Cmd
	t     *testing.T       // of stand: a Wait that fails fails it
	ended chan struct{}    // closed once the sleep is reaped
	state *os.ProcessState // how it ended, once ended is closed
	err   error
}

func newForeground(t *testing.T, p *Proxy) *foreground {
	t.Helper()
	f := &foreground{}
	f.group.Store(int64(syscall.Getpgrp()))
	p.mu.Lock()
	p.fg = func() (int, error) { return int(f.group.Load()), nil }
	p.mu.Unlock()
	return f
}

// stand starts sleep in a group of its own and puts it in the foreground.
// One goroutine waits for it, however many times interrupted asks: of two
// Waits of the process only the one that reaps it gets its status, the
// other "no child processes".
func (f *foreground) stand(t *testing.T) {
	t.Helper()
	f.cmd = exec.Command("sleep", "30")
	f.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := f.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	f.t, f.ended = t, make(chan struct{})
	go func() {
		f.state, f.err = f.cmd.Process.Wait()
		close(f.ended)
	}()
	t.Cleanup(func() { _ = f.cmd.Process.Kill() })
	f.group.Store(int64(f.cmd.Process.Pid))
}

// interrupted reports whether the sleep ended by SIGINT within d.
func (f *foreground) interrupted(d time.Duration) bool {
	f.t.Helper()
	select {
	case <-f.ended:
		if f.err != nil {
			f.t.Fatalf("waiting for the sleep: %v", f.err)
		}
		ws, ok := f.state.Sys().(syscall.WaitStatus)
		return ok && ws.Signaled() && ws.Signal() == syscall.SIGINT
	case <-time.After(d):
		return false
	}
}

// back makes the test's group the foreground again, for the RPC calls.
func (f *foreground) back() { f.group.Store(int64(syscall.Getpgrp())) }

// Esc while the shell runs the agent's command: the key goes nowhere, the
// command gets SIGINT once $AISH_RUN/esc names it, and the output the
// shell then sends, cut short, reaches the model with "[interrupted by the
// user]"; the request goes on. Alt+B and an arrow cut after its ESC go on
// to the shell, so does Esc to a full-screen program the command runs.
func TestEscCommand(t *testing.T) {
	prov := &scripted{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "bash", Args: json.RawMessage(`{"command":"sleep 30"}`)}}},
		{Text: "stopped"},
	}}
	p, _, cwd := hosted(t, prov)
	fg := newForeground(t, p)
	p.marker(Marker{Kind: "ask-start"})
	if _, err := call(t, p, rpc.MethodAgentStart, rpc.AgentParams{Text: "wait", Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	p.marker(Marker{Kind: "agent-start", Payload: "c1;sleep 30"})
	p.output([]byte("waiting\r\n"))

	if got := p.key([]byte("\x1bb")); string(got) != "\x1bb" {
		t.Errorf("Alt+B gave the shell %q", got)
	}
	if got := p.key([]byte("\x1b")); len(got) != 0 {
		t.Errorf("the ESC of a cut arrow gave the shell %q at once", got)
	}
	if got := p.key([]byte("[A")); string(got) != "\x1b[A" {
		t.Errorf("an arrow cut after its ESC gave the shell %q", got)
	}
	if b, _ := os.ReadFile(filepath.Join(p.run, "esc")); len(b) != 0 {
		t.Fatalf("stopped by keys that are not Esc: esc holds %q", b)
	}

	fg.stand(t)
	if got := p.key([]byte("\x1b")); len(got) != 0 {
		t.Errorf("Esc gave the shell %q", got)
	}
	if !fg.interrupted(5 * time.Second) {
		t.Fatal("the command got no SIGINT")
	}
	fg.back()
	if b, _ := os.ReadFile(filepath.Join(p.run, "esc")); string(b) != "c1 130\n" {
		t.Errorf("esc holds %q", b)
	}

	// The shell ends the command as the file says and resumes the agent.
	p.marker(Marker{Kind: "agent-end", Payload: "c1;130;" + cwd})
	if b, _ := os.ReadFile(filepath.Join(p.run, "esc")); len(b) != 0 {
		t.Errorf("esc left with %q", b)
	}
	if _, err := call(t, p, rpc.MethodAgentResume, rpc.AgentParams{ID: "c1", RC: 130, Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	if got := journalKinds(p.sess); got != "user assistant tool_result assistant" {
		t.Fatalf("journal %s", got)
	}
	r := p.sess.Entries()[2].Output
	if want := "waiting\n[exit 130, cwd " + cwd + "]\n[interrupted by the user]"; r != want {
		t.Errorf("result %q, want %q", r, want)
	}
}

// A program of the agent's command that has the terminal gets Esc: one on
// the alternate screen, or one that reads the keys as they come.
func TestEscToProgram(t *testing.T) {
	prov := &scripted{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "bash", Args: json.RawMessage(`{"command":"vi"}`)}}},
	}}
	p, _, cwd := hosted(t, prov)
	p.marker(Marker{Kind: "ask-start"})
	if _, err := call(t, p, rpc.MethodAgentStart, rpc.AgentParams{Text: "edit", Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	p.marker(Marker{Kind: "agent-start", Payload: "c1;vi"})
	p.output([]byte("\x1b[?1049h"))
	if got := p.key([]byte("\x1b")); string(got) != "\x1b" {
		t.Errorf("Esc to a full-screen program gave it %q", got)
	}
	p.output([]byte("\x1b[?1049l"))
	lines := true
	p.mu.Lock()
	p.lines = func() bool { return lines }
	p.mu.Unlock()
	lines = false
	if got := p.key([]byte("\x1b")); string(got) != "\x1b" {
		t.Errorf("Esc to a program reading the keys gave it %q", got)
	}
	if b, _ := os.ReadFile(filepath.Join(p.run, "esc")); len(b) != 0 {
		t.Errorf("the command was stopped: esc holds %q", b)
	}
}

// Esc before the shell has started the command it was handed: the signal
// waits for its agent-start.
func TestEscBeforeCommand(t *testing.T) {
	prov := &scripted{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "bash", Args: json.RawMessage(`{"command":"sleep 30"}`)}}},
	}}
	p, _, cwd := hosted(t, prov)
	fg := newForeground(t, p)
	p.marker(Marker{Kind: "ask-start"})
	if _, err := call(t, p, rpc.MethodAgentStart, rpc.AgentParams{Text: "wait", Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	fg.stand(t)
	p.key([]byte("\x1b"))
	if fg.interrupted(3 * escWait) {
		t.Fatal("SIGINT before the command started")
	}
	p.marker(Marker{Kind: "agent-start", Payload: "c1;sleep 30"})
	if !fg.interrupted(5 * time.Second) {
		t.Fatal("the command got no SIGINT on its agent-start")
	}
	fg.back()
	// The prompt after Ctrl+C on it ends the stop with the request.
	p.marker(Marker{Kind: "cmd-end", Payload: "130;" + cwd})
	if b, _ := os.ReadFile(filepath.Join(p.run, "esc")); len(b) != 0 || p.stop != nil {
		t.Errorf("the stop outlived the request: esc %q", b)
	}
}

// Esc during a call in the proxy, an external tool, stops the call alone:
// the model gets what it printed and goes on.
func TestEscTool(t *testing.T) {
	prov := &scripted{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "slow", Args: json.RawMessage(`{}`)}}},
		{Text: "went on"},
	}}
	p, out, cwd := hosted(t, prov)
	home := filepath.Dir(cwd)
	dir := filepath.Join(home, ".config", "aish", "tools")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	started := filepath.Join(home, "started")
	script := "#!/bin/sh\n# aish:desc Slow\necho early\n: >" + started + "\nsleep 30\necho late\n"
	if err := os.WriteFile(filepath.Join(dir, "slow"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	p.marker(Marker{Kind: "ask-start"})
	done := make(chan error, 1)
	go func() {
		_, err := call(t, p, rpc.MethodAgentStart, rpc.AgentParams{Text: "go", Cwd: cwd})
		done <- err
	}()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the tool did not start: %q", out.String())
		}
	}
	if got := p.key([]byte("\x1b")); len(got) != 0 {
		t.Errorf("Esc gave the shell %q", got)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the tool was not stopped")
	}
	if got := journalKinds(p.sess); got != "user assistant tool_result assistant" {
		t.Fatalf("journal %s", got)
	}
	if r := p.sess.Entries()[2].Output; r != "early\n[interrupted by the user]" {
		t.Errorf("result %q", r)
	}
}

// Esc in a turn of the model ends the request, with no error for the
// client to print; the agent waits for the user.
func TestEscTurn(t *testing.T) {
	prov := &scripted{block: true}
	p, _, cwd := hosted(t, prov)
	p.marker(Marker{Kind: "ask-start"})
	done := make(chan error, 1)
	go func() {
		_, err := call(t, p, rpc.MethodAgentStart, rpc.AgentParams{Text: "wait", Cwd: cwd})
		done <- err
	}()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		p.mu.Lock()
		running := p.stopReq != nil
		p.mu.Unlock()
		if running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the request never started")
		}
	}
	p.key([]byte("\x1b"))
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("the request ended with %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the turn goes on after Esc")
	}
	if prov.calls != 1 {
		t.Errorf("%d turns", prov.calls)
	}
}

// The viewer keeps its Esc: it closes, the request goes on.
func TestEscViewer(t *testing.T) {
	prov := &scripted{block: true}
	p, _, cwd := hosted(t, prov)
	p.size = func() (int, int) { return 80, 24 }
	p.marker(Marker{Kind: "ask-start"})
	done := make(chan error, 1)
	go func() {
		_, err := call(t, p, rpc.MethodAgentStart, rpc.AgentParams{Text: "wait", Cwd: cwd})
		done <- err
	}()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		p.mu.Lock()
		running := p.stopReq != nil
		p.mu.Unlock()
		if running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the request never started")
		}
	}
	p.mu.Lock()
	p.folds = []Fold{{Title: "❯ ls", Text: "a\nb\n"}}
	p.mu.Unlock()
	p.key([]byte{ctrlO})
	p.mu.Lock()
	open := p.view != nil
	p.mu.Unlock()
	if !open {
		t.Fatal("Ctrl+O opened no viewer")
	}
	p.key([]byte("\x1b"))
	time.Sleep(3 * escWait)
	p.mu.Lock()
	open = p.view != nil
	p.mu.Unlock()
	if open {
		t.Error("Esc left the viewer open")
	}
	select {
	case err := <-done:
		t.Fatalf("the request ended with the viewer's Esc: %v", err)
	default:
	}
	p.cancelRequest()
	<-done
	if !strings.Contains(journalKinds(p.sess), "user") {
		t.Errorf("journal %s", journalKinds(p.sess))
	}
}
