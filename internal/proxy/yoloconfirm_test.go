package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/GoldenDeals/aish/internal/rpc"
)

// firmNow makes the keys of a firm question count at once.
func firmNow(t *testing.T) {
	t.Helper()
	old := firmWait
	firmWait = 0
	t.Cleanup(func() { firmWait = old })
}

// askYolo calls rpc yolo on, with ctx, as the shell's foreground job at a
// terminal, and waits for its question; the call's error comes on the
// channel.
func askYolo(t *testing.T, p *Proxy, ctx context.Context) <-chan error {
	t.Helper()
	p.mu.Lock()
	if p.size == nil {
		p.size = func() (int, int) { return 200, 24 }
	}
	p.mu.Unlock()
	res := make(chan error, 1)
	go func() {
		b, _ := json.Marshal(rpc.YoloParams{On: true})
		_, err := p.handle(rpc.WithPeer(ctx, os.Getpid()), rpc.MethodYolo, b)
		res <- err
	}()
	waitOpen(t, p, func() bool { return p.ask != nil })
	return res
}

// answered is the error of the call askYolo made, once it returns.
func answered(t *testing.T, res <-chan error) error {
	t.Helper()
	select {
	case err := <-res:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("rpc yolo never returned")
		return nil
	}
}

// yoloByUser turns yolo on as the user does: aish yolo, then y.
func yoloByUser(t *testing.T, p *Proxy) {
	t.Helper()
	firmNow(t)
	res := askYolo(t, p, context.Background())
	pause(p) // a Yes comes after one (askguard.go)
	p.key([]byte("y"))
	if err := answered(t, res); err != nil || !p.yoloOn() {
		t.Fatalf("yolo by the user: %v, on %v", err, p.yoloOn())
	}
}

// aish yolo asks before the checks go off, No chosen at first: y, or Yes
// chosen with Enter, turns yolo on; n, Enter, Esc, Alt with a key or a
// command typed ahead leave it off, the call refused. No key goes on to
// the shell.
func TestYoloConfirm(t *testing.T) {
	firmNow(t)
	for _, tc := range []struct {
		name string
		keys []string // each after a pause (askguard.go)
		on   bool
	}{
		{"y", []string{"y"}, true},
		{"Y", []string{"Y"}, true},
		{"left, Enter", []string{"\x1b[D", "\r"}, true},
		{"n", []string{"n"}, false},
		{"Enter", []string{"\r"}, false},
		{"Esc", []string{"\x1b"}, false},
		{"Alt+y", []string{"\x1by"}, false},
		{"a command typed ahead", []string{"ls -la\r"}, false},
		{"left, a command typed ahead", []string{"\x1b[Dhtop\r"}, false},
	} {
		p, out, _ := hosted(t, &scripted{})
		res := askYolo(t, p, context.Background())
		if s := out.String(); !strings.Contains(s, "Turn the checks off?\x1b[0m "+choices("No")) ||
			!strings.Contains(s, "the guard and the hooks stay") {
			t.Errorf("%s: the question %q", tc.name, s)
		}
		for _, k := range tc.keys {
			pause(p)
			if pass := p.key([]byte(k)); len(pass) > 0 {
				t.Errorf("%s: to the shell %q", tc.name, pass)
			}
		}
		err := answered(t, res)
		if tc.on && (err != nil || !p.yoloOn()) || !tc.on && (!errors.Is(err, errYoloDeclined) || p.yoloOn()) {
			t.Errorf("%s: %v, on %v", tc.name, err, p.yoloOn())
		}
		if p.ask != nil {
			t.Errorf("%s: the question stayed open", tc.name)
		}
	}
}

// The question of aish yolo ends with the call: Ctrl+C reaches the client,
// which gives up and closes the connection; the checks stay on.
func TestYoloConfirmCancel(t *testing.T) {
	firmNow(t)
	p, _, _ := hosted(t, &scripted{})
	ctx, cancel := context.WithCancel(context.Background())
	res := askYolo(t, p, ctx)
	if pass := p.key([]byte{0x03}); string(pass) != "\x03" {
		t.Errorf("Ctrl+C to the shell: %q", pass)
	}
	p.mu.Lock()
	open := p.ask != nil
	p.mu.Unlock()
	if !open {
		t.Fatal("Ctrl+C closed the question before the client gave up")
	}
	cancel()
	if err := answered(t, res); !errors.Is(err, context.Canceled) || p.yoloOn() {
		t.Errorf("%v, on %v", err, p.yoloOn())
	}
	if p.ask != nil {
		t.Error("the question stayed open")
	}
}

// Unanswered in yoloWait, the question is left with No and why.
func TestYoloConfirmDeadline(t *testing.T) {
	old := yoloWait
	yoloWait = 30 * time.Millisecond
	t.Cleanup(func() { yoloWait = old })
	p, out, _ := hosted(t, &scripted{})
	err := answered(t, askYolo(t, p, context.Background()))
	if err == nil || !strings.Contains(err.Error(), "no answer in 30ms") || p.yoloOn() {
		t.Errorf("%v, on %v", err, p.yoloOn())
	}
	if !strings.Contains(out.String(), "No (no answer in 30ms)") {
		t.Errorf("terminal %q", out.String())
	}
}

// The keys of the question's first firmWait were typed for the prompt
// before it showed: none answers it, Ctrl+C still goes to the shell.
func TestYoloConfirmTypedAhead(t *testing.T) {
	old := firmWait
	firmWait = 300 * time.Millisecond
	t.Cleanup(func() { firmWait = old })
	p, _, _ := hosted(t, &scripted{})
	res := askYolo(t, p, context.Background())
	start := time.Now()
	for _, k := range []string{"y", "\x1b[D\r"} {
		if pass := p.key([]byte(k)); len(pass) > 0 {
			t.Errorf("%q to the shell: %q", k, pass)
		}
	}
	if pass := p.key([]byte("yes\x03")); string(pass) != "\x03" {
		t.Errorf("Ctrl+C: %q", pass)
	}
	p.key([]byte("\x1b"))
	time.Sleep(2 * escWait) // the Esc, held for a sequence, is taken alone by now
	select {
	case err := <-res:
		t.Fatalf("answered by keys typed ahead, %v after the question: %v", time.Since(start), err)
	default:
	}
	if since := time.Since(start); since < firmWait {
		time.Sleep(firmWait - since)
	}
	pause(p) // a Yes comes after one (askguard.go)
	p.key([]byte("y"))
	if err := answered(t, res); err != nil || !p.yoloOn() {
		t.Errorf("%v, on %v", err, p.yoloOn())
	}
}

// What a process in the shell writes to its terminal is the shell's
// output, not keys: it does not answer the question.
func TestYoloConfirmNotFromPTY(t *testing.T) {
	firmNow(t)
	p, out, _ := hosted(t, &scripted{})
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
	res := askYolo(t, p, context.Background())
	if _, err := tty.Write([]byte("y\r\n\x1b[D\ry")); err != nil {
		t.Fatal(err)
	}
	// The output waits for the answer (askhold.go).
	for deadline := time.Now().Add(5 * time.Second); !strings.HasSuffix(heldOutput(p), "\x1b[D\ry"); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the PTY's output never came: %q", heldOutput(p))
		}
	}
	if s := out.String(); strings.Contains(s, "\x1b[D\ry") {
		t.Errorf("drawn over the question: %q", s)
	}
	select {
	case err := <-res:
		t.Fatalf("answered by the PTY: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if p.yoloOn() {
		t.Fatal("on by the PTY")
	}
	p.key([]byte("n"))
	if err := answered(t, res); !errors.Is(err, errYoloDeclined) {
		t.Errorf("then n: %v", err)
	}
	if s := modeless(out.String()); !strings.HasSuffix(s, "No\x1b[K\r\n\x1b[?25hy\r\r\n\x1b[D\ry") {
		t.Errorf("the output after the answer: %q", s)
	}
}

// aish yolo off, and aish yolo while it is on, ask nothing; with no
// terminal to ask on, yolo stays off.
func TestYoloConfirmNotAsked(t *testing.T) {
	p, out, _ := hosted(t, &scripted{})
	if _, err := call(t, p, rpc.MethodYolo, rpc.YoloParams{On: true}); err == nil || !strings.Contains(err.Error(), "no terminal") || p.yoloOn() {
		t.Errorf("no terminal: %v, on %v", err, p.yoloOn())
	}
	p.mu.Lock()
	p.size = func() (int, int) { return 200, 24 }
	p.yolo = true
	p.mu.Unlock()
	if _, err := call(t, p, rpc.MethodYolo, rpc.YoloParams{On: true}); err != nil || !p.yoloOn() {
		t.Errorf("on again: %v, on %v", err, p.yoloOn())
	}
	if _, err := call(t, p, rpc.MethodYolo, rpc.YoloParams{}); err != nil || p.yoloOn() {
		t.Errorf("off: %v, on %v", err, p.yoloOn())
	}
	if s := out.String(); strings.Contains(s, "Turn the checks off?") || p.ask != nil {
		t.Errorf("asked: %q", s)
	}
}
