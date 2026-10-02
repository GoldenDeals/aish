package proxy

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// testShell is `bash -c` in a group of its own, as the shell under a PTY
// leads one.
type testShell struct {
	cmd    *exec.Cmd
	exited chan struct{} // Wait returned
	closed chan struct{} // stdout ended: the shell and all it started are gone
}

// startShell starts script and returns once it has printed its first
// line, so the signals a test sends come after the traps are set.
func startShell(t *testing.T, script string) *testShell {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bash, "-c", script)
	cmd.Stdout = w
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	w.Close()
	s := &testShell{cmd: cmd, exited: make(chan struct{}), closed: make(chan struct{})}
	go func() {
		cmd.Wait()
		close(s.exited)
	}()
	t.Cleanup(func() {
		select {
		case <-s.exited:
		default:
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-s.exited
		}
		r.Close()
	})
	ready := make(chan struct{})
	go func() {
		br := bufio.NewReader(r)
		br.ReadString('\n')
		close(ready)
		io.Copy(io.Discard, br)
		close(s.closed)
	}()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("the shell did not start")
	}
	return s
}

func (s *testShell) waitExit(t *testing.T, within time.Duration) {
	t.Helper()
	select {
	case <-s.exited:
	case <-time.After(within):
		t.Fatalf("the shell is still there after %s", within)
	}
}

func (s *testShell) killed() bool {
	ws, ok := s.cmd.ProcessState.Sys().(syscall.WaitStatus)
	return ok && ws.Signaled() && ws.Signal() == syscall.SIGKILL
}

func TestForwardSignalsHangsUp(t *testing.T) {
	s := startShell(t, `trap "" TERM; sleep 30 & pid=$!; trap 'kill $pid; exit 0' HUP; echo ready; wait`)
	sigs := make(chan os.Signal, 2)
	stop := forwardSignals(sigs, s.cmd.Process, 10*time.Second)
	sigs <- syscall.SIGTERM
	s.waitExit(t, 2*time.Second)
	stop()
	if code := s.cmd.ProcessState.ExitCode(); code != 0 {
		t.Errorf("exit code %d, want 0 from the HUP trap", code)
	}
}

func TestForwardSignalsKillsAfterGrace(t *testing.T) {
	s := startShell(t, `trap "" TERM HUP; sleep 30 & echo ready; wait`)
	sigs := make(chan os.Signal, 2)
	const grace = 200 * time.Millisecond
	stop := forwardSignals(sigs, s.cmd.Process, grace)
	defer stop()
	start := time.Now()
	sigs <- syscall.SIGTERM
	s.waitExit(t, 5*time.Second)
	if d := time.Since(start); d < grace {
		t.Errorf("killed after %s, before the grace of %s", d, grace)
	}
	if !s.killed() {
		t.Errorf("the shell ended with %v, not SIGKILL", s.cmd.ProcessState)
	}
	select {
	case <-s.closed:
	case <-time.After(2 * time.Second):
		t.Error("the shell's sleep outlived it")
	}
}

func TestForwardSignalsKillsOnSecond(t *testing.T) {
	s := startShell(t, `trap "" TERM HUP; sleep 30 & echo ready; wait`)
	sigs := make(chan os.Signal, 2)
	stop := forwardSignals(sigs, s.cmd.Process, 10*time.Second)
	defer stop()
	sigs <- syscall.SIGTERM
	sigs <- syscall.SIGTERM
	s.waitExit(t, 2*time.Second)
	if !s.killed() {
		t.Errorf("the shell ended with %v, not SIGKILL", s.cmd.ProcessState)
	}
	select {
	case <-s.closed:
	case <-time.After(2 * time.Second):
		t.Error("the shell's sleep outlived it")
	}
}

func TestForwardSignalsStop(t *testing.T) {
	// A shell that SIGHUP ends: whatever is sent after stop would show.
	s := startShell(t, `sleep 30 & echo ready; wait`)
	sigs := make(chan os.Signal, 2)
	const grace = 100 * time.Millisecond
	stop := forwardSignals(sigs, s.cmd.Process, grace)
	stop()
	stop()
	sigs <- syscall.SIGTERM
	sigs <- syscall.SIGHUP
	select {
	case <-s.exited:
		t.Fatalf("a signal reached the shell after stop: %v", s.cmd.ProcessState)
	case <-time.After(3 * grace):
	}

	gone := startShell(t, `echo ready`)
	gone.waitExit(t, 2*time.Second)
	sigs = make(chan os.Signal, 2)
	stop = forwardSignals(sigs, gone.cmd.Process, grace)
	stop()
	stop()
}
