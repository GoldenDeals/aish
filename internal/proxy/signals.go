package proxy

import (
	"os"
	"sync"
	"syscall"
	"time"
)

// shutdownGrace is how long the shell has to exit on the hangup it is
// sent for a signal to aish before it is killed.
const shutdownGrace = 3 * time.Second

// forwardSignals makes aish's SIGHUP and SIGTERM end the shell. The shell
// gets SIGHUP for either, as when the terminal closes: an interactive bash
// ignores SIGTERM. If it is still there after grace, or another signal
// comes, its process group is killed. The returned stop ends the
// forwarding once the shell is gone; it waits for the forwarding to end,
// so nothing is sent to a pid that may already belong to someone else.
func forwardSignals(sigs <-chan os.Signal, proc *os.Process, grace time.Duration) (stop func()) {
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		var timer *time.Timer
		var deadline <-chan time.Time
		defer func() {
			if timer != nil {
				timer.Stop()
			}
		}()
		killed := false
		kill := func() {
			killed, deadline = true, nil
			// The shell leads its own session and group (pty.Start). Its
			// jobs get groups of their own, but the kernel hangs up the
			// terminal's foreground job once the session leader is dead.
			if err := syscall.Kill(-proc.Pid, syscall.SIGKILL); err != nil {
				_ = proc.Kill()
			}
		}
		// stop wins over a signal or the deadline that came with it: the
		// shell may be reaped already.
		stopped := func() bool {
			select {
			case <-done:
				return true
			default:
				return false
			}
		}
		for {
			select {
			case <-done:
				return
			case <-sigs:
				switch {
				case stopped():
					return
				case killed:
				case timer == nil:
					// On SIGHUP bash hangs up its jobs itself before it
					// exits.
					_ = proc.Signal(syscall.SIGHUP)
					timer = time.NewTimer(grace)
					deadline = timer.C
				default:
					kill()
				}
			case <-deadline:
				if stopped() {
					return
				}
				kill()
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(done) })
		<-finished
	}
}
