package agent

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"golang.org/x/term"
)

const (
	spinnerTick = 80 * time.Millisecond
	// spinnerIdle is how long the model must be silent before the spinner
	// comes back, e.g. while it writes a long tool call after some text.
	spinnerIdle = 600 * time.Millisecond
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// spinner shows "thinking" while the model produces nothing visible. Text
// written through it erases the spinner first; the spinner only appears at
// the start of a line, so it never mangles streamed text.
type spinner struct {
	mu    sync.Mutex
	w     io.Writer
	shown bool
	bol   bool // the cursor is at the beginning of a line
	last  time.Time
	start time.Time
	frame int
	stop  chan struct{}
	done  chan struct{}
}

func startSpinner(w io.Writer) *spinner {
	s := &spinner{w: w, bol: true, start: time.Now(), stop: make(chan struct{}), done: make(chan struct{})}
	if f, ok := w.(*os.File); !ok || !term.IsTerminal(int(f.Fd())) {
		close(s.done)
		return s
	}
	go s.run()
	return s
}

func (s *spinner) run() {
	defer close(s.done)
	t := time.NewTicker(spinnerTick)
	defer t.Stop()
	s.draw()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			s.draw()
		}
	}
}

func (s *spinner) draw() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.bol || (!s.last.IsZero() && time.Since(s.last) < spinnerIdle) {
		return
	}
	if !s.shown {
		s.start = time.Now()
	}
	s.frame = (s.frame + 1) % len(spinnerFrames)
	label := "thinking"
	if d := time.Since(s.start); d >= 3*time.Second {
		label = fmt.Sprintf("thinking %ds", int(d.Seconds()))
	}
	// Hide the cursor while the spinner is on the line.
	fmt.Fprintf(s.w, "\r\x1b[?25l%s%s %s…%s\x1b[K", cyan, spinnerFrames[s.frame], dim+label, reset)
	s.shown = true
}

func (s *spinner) erase() {
	if s.shown {
		io.WriteString(s.w, "\r\x1b[K\x1b[?25h")
		s.shown = false
	}
}

func (s *spinner) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.erase()
	if len(p) > 0 {
		s.bol = p[len(p)-1] == '\n'
		s.last = time.Now()
	}
	return s.w.Write(p)
}

// Stop removes the spinner. It is safe to call more than once.
func (s *spinner) Stop() {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	s.erase()
}
