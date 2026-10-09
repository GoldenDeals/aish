package agent

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/GoldenDeals/aish/internal/capture"
	"github.com/GoldenDeals/aish/internal/rpc"
)

// What the user sees of the subagents in the background (bgtask.go):
// their work does not go to the screen, so `aish tasks` lists them and
// prints the output of one, through the proxy, which calls these from the
// goroutine of an rpc while a request may be in progress: they read the
// set under its locks alone. And task_wait, which may wait for minutes,
// shows whom it waits for on the live output of its call.

// BackgroundTasks are the subagents in the background, as started: those
// at work and the finished ones kept.
func (a *Agent) BackgroundTasks() []rpc.Task {
	s := a.madeBackground()
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]rpc.Task, len(s.order))
	for i, id := range s.order {
		out[i] = s.jobs[id].task()
	}
	return out
}

// BackgroundTask is subagent id in the background with what it has shown
// so far, as plain text: its calls, its commands' output, its answer. An
// id not kept is an error, as for task_result.
func (a *Agent) BackgroundTask(id string) (rpc.Task, error) {
	s := a.madeBackground()
	if s == nil {
		s = &bgSet{} // none was ever started: the id is unknown
	}
	s.mu.Lock()
	j := s.jobs[id]
	if j == nil {
		defer s.mu.Unlock()
		return rpc.Task{}, errors.New(s.missing(id))
	}
	// Under s.mu, so that a finished one's output is whole: it finishes
	// under s.mu, once its last write is done.
	t, raw := j.task(), j.out.bytes()
	s.mu.Unlock()
	t.Output = capture.Clean(raw)
	return t, nil
}

// task is j as `aish tasks` lists it. Called under the set's mu.
func (j *bgJob) task() rpc.Task {
	return rpc.Task{ID: j.id, Agent: j.name, Desc: j.desc, State: j.state, Prompt: j.prompt}
}

func (o *bgOutput) bytes() []byte {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.Bytes()
}

// waitTick is how often task_wait redraws its line: the seconds it shows.
const waitTick = time.Second

// waitLine is task_wait's line on the live output of its call while it
// waits, redrawn in place, and taken off when the wait is over, so that
// the answers take its place. A redraw covers the longer text before it
// with spaces rather than erasing it: the proxy keeps the live output for
// Ctrl+O, and turns it into text as a terminal would show it, without the
// erasing sequences.
type waitLine struct {
	w     io.Writer
	cols  int
	start time.Time
	shown string
	width int // the widest text drawn
}

// waitLine is the line on live, nil without live or a terminal: as with
// the spinner, plain output gets no line redrawn in place.
func (a *Agent) waitLine(live io.Writer) *waitLine {
	if live == nil {
		return nil
	}
	cols, _ := a.UI.Size()
	if cols <= 0 {
		return nil
	}
	return &waitLine{w: live, cols: cols, start: time.Now()}
}

// draw shows that the wait is for ids. A nil line draws nothing.
func (l *waitLine) draw(ids []string) {
	if l == nil {
		return
	}
	text := "waiting for " + strings.Join(ids, ", ") + "…"
	if d := time.Since(l.start); d >= waitTick {
		text += fmt.Sprintf(" %ds", int(d/time.Second))
	}
	// The last column stays free, as with the statuses the proxy draws:
	// a line that wraps would not be redrawn from its start.
	text = runewidth.Truncate(text, l.cols-1, "…")
	if text == l.shown {
		return
	}
	w := runewidth.StringWidth(text)
	pad := strings.Repeat(" ", max(l.width-w, 0))
	io.WriteString(l.w, "\r"+dim+text+reset+pad)
	l.shown, l.width = text, max(l.width, w)
}

// clear takes the line off, leaving the cursor at the start of its empty
// line. A nil line has nothing to take off.
func (l *waitLine) clear() {
	if l == nil || l.width == 0 {
		return
	}
	io.WriteString(l.w, "\r"+strings.Repeat(" ", l.width)+"\r\x1b[K")
	l.shown, l.width = "", 0
}
