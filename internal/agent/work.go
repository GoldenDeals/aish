package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"

	"github.com/inebotov/aish/internal/capture"
	"github.com/inebotov/aish/internal/session"
	"github.com/inebotov/aish/internal/tools"
)

// With hide_work the calls of a request are not shown one by one: a run of
// them is summed up in one line, "Read 3 files, ran 2 commands", redrawn in
// place as they come and while the model thinks, and each is kept for
// Ctrl+O by the UI (Hider). What the user has to see is not hidden and
// ends the run: a question, a form, a denial, an error, the panes of
// subagents, a line the agent prints of itself, the text of the reply. The
// journal, the policy, the hooks and what the model gets are the same.
//
// The line of an open group is the last on the screen, the cursor on it:
// drawn by call, or by the spinner of a turn in its place. To keep it so,
// the agent's UI is a workUI while there is a group, which ends the line
// before anything else is printed; the group and the spinner draw on the
// UI beneath it. A command handed to the shell leaves the line open, the
// proxy drawing nothing of it, and Resume goes on with the same group.

// workKind is what a call does, as the line of the group counts it.
type workKind int

const (
	workRead workKind = iota
	workEdit
	workRun
	workSkill
	workTool
)

// workWords say what a kind of call does, as it goes on and once done, and
// to what, one and many.
var workWords = [...]struct{ doing, did, one, many string }{
	workRead:  {"reading", "read", "file", "files"},
	workEdit:  {"editing", "edited", "file", "files"},
	workRun:   {"running", "ran", "command", "commands"},
	workSkill: {"using", "used", "skill", "skills"},
	workTool:  {"calling", "called", "tool", "tools"},
}

// kindOf is the kind of a call of t. The file tools are told by name, but
// not a user's or a server's tool of that name.
func kindOf(t tools.Tool) workKind {
	switch {
	case handsOff(t):
		return workRun
	case reflect.TypeOf(t) == skillTool:
		return workSkill
	case tools.Streams(t) || tools.ServerOf(t) != "":
		return workTool
	}
	switch t.Name() {
	case "read_file":
		return workRead
	case "write_file", "edit_file":
		return workEdit
	}
	return workTool
}

// workHint ends the line of a group once it is done: the calls are in
// Ctrl+O.
const workHint = "  (ctrl+o to expand)"

// workGroup is the run of calls summed up in one line.
type workGroup struct {
	ui    UI // beneath the workUI: what the group and the spinner draw on
	hider Hider

	mu     sync.Mutex
	counts map[workKind]int
	order  []workKind // by the first call of each
	drawn  bool       // its line is on the screen, or the spinner's in its place
	// waiting is set while the shell runs a command of the group: the
	// line waits for Resume to go on with it.
	waiting bool
	frame   int
}

// add counts a call of kind k, which begins.
func (g *workGroup) add(k workKind) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.counts == nil {
		g.counts = map[workKind]int{}
	}
	if g.counts[k] == 0 {
		g.order = append(g.order, k)
	}
	g.counts[k]++
}

// drop takes back a call of kind k: it failed, and is shown instead.
func (g *workGroup) drop(k workKind) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.counts[k] == 0 {
		return
	}
	if g.counts[k]--; g.counts[k] == 0 {
		g.order = slices.DeleteFunc(g.order, func(o workKind) bool { return o == k })
	}
}

// Label sums the calls up: as they go on when active ("Reading 3 files,
// running 2 commands"), as done otherwise ("Read 3 files, ran 2
// commands"). "" for none.
func (g *workGroup) Label(active bool) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.label(active)
}

func (g *workGroup) label(active bool) string {
	parts := make([]string, 0, len(g.order))
	for _, k := range g.order {
		w, n := workWords[k], g.counts[k]
		verb, what := w.did, w.many
		if active {
			verb = w.doing
		}
		if n == 1 {
			what = w.one
		}
		parts = append(parts, fmt.Sprintf("%s %d %s", verb, n, what))
	}
	s := strings.Join(parts, ", ")
	if s == "" {
		return ""
	}
	r, size := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[size:]
}

// fit cuts s to w columns, none without a terminal.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	return runewidth.Truncate(s, w, "")
}

// active is the line of the group as its calls go on, drawn as the spinner
// draws its own, with frame: within cols-1 columns, the last being kept
// free as everywhere.
func (g *workGroup) active(frame string, cols int) string {
	label := g.label(true)
	if cols > 0 {
		label = fit(label, cols-1-runewidth.StringWidth(frame+" …"))
	}
	return fmt.Sprintf("%s%s %s…%s", cyan, frame, dim+label, reset)
}

// done is the line of the group once it is over. Short of room, the hint
// goes first, then the end of the label.
func (g *workGroup) done(cols int) string {
	label := g.label(false)
	hint := dim + workHint + reset
	if w := cols - 1 - runewidth.StringWidth("● "); cols > 0 && runewidth.StringWidth(label+workHint) > w {
		label, hint = runewidth.Truncate(label, max(w, 0), "…"), ""
	}
	return cyan + "●" + reset + " " + label + hint
}

// draw shows the group as its calls go on, in place of its line or of the
// spinner's.
func (g *workGroup) draw() {
	g.mu.Lock()
	defer g.mu.Unlock()
	cols, _ := g.ui.Size()
	g.frame = (g.frame + 1) % len(spinnerFrames)
	fmt.Fprintf(g.ui, "\r%s\x1b[K", g.active(spinnerFrames[g.frame], cols))
	g.drawn = true
}

// spinLabel is what the spinner of a turn shows over the open group, ""
// when none is open: it then says it is thinking.
func (g *workGroup) spinLabel() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.order) == 0 {
		return ""
	}
	cols, _ := g.ui.Size()
	label := g.label(true)
	if cols > 0 {
		label = fit(label, cols-1-runewidth.StringWidth("⠋ …"))
	}
	return label
}

// close ends the group: its line, done, stays above whatever comes next,
// and the next call begins another. A group with no calls left takes its
// line off; one never drawn prints nothing.
func (g *workGroup) close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.drawn {
		if len(g.order) == 0 {
			io.WriteString(g.ui, "\r\x1b[K")
		} else {
			cols, _ := g.ui.Size()
			io.WriteString(g.ui, "\r"+g.done(cols)+"\x1b[K\n")
		}
	}
	g.counts, g.order, g.drawn, g.waiting = nil, nil, false, false
}

func (g *workGroup) wait(on bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.waiting = on
}

func (g *workGroup) isWaiting() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.waiting
}

// workUI is the agent's UI while there may be a group: what the agent
// prints, asks or shows by itself ends the group's line first.
type workUI struct {
	UI
	g *workGroup
}

func (u workUI) Write(b []byte) (int, error) {
	u.g.close()
	return u.UI.Write(b)
}

func (u workUI) Ask(ctx context.Context, q string) (string, error) {
	u.g.close()
	return u.UI.Ask(ctx, q)
}

func (u workUI) Form(ctx context.Context, qs []Question) ([]Answer, error) {
	u.g.close()
	return u.UI.Form(ctx, qs)
}

func (u workUI) Fold(title, text string) {
	u.g.close()
	u.UI.Fold(title, text)
}

func (u workUI) Live(title string) Live {
	u.g.close()
	return u.UI.Live(title)
}

func (u workUI) CommandAt(col int, long bool, hidden int) {
	u.g.close()
	u.UI.CommandAt(col, long, hidden)
}

// workPanes is a workUI over a UI with panes: the task tool looks for them.
type workPanes struct {
	workUI
	panes Panes
}

func (u workPanes) Pane(title string) Live {
	u.g.close()
	return u.panes.Pane(title)
}

func (u workPanes) ClosePanes() { u.panes.ClosePanes() }

func wrapWork(ui UI, g *workGroup) UI {
	w := workUI{UI: ui, g: g}
	if p, ok := ui.(Panes); ok {
		return workPanes{w, p}
	}
	return w
}

// hideWork puts a workUI over the UI for a step of a request, Start's
// (fresh) or Resume's, when hide_work is on and the UI can keep what is
// not drawn, and takes it off when it is not. fresh forgets the group a
// request left waiting for a command the shell did not finish: the proxy
// ended its line (Proxy.marker, cmd-end).
func (a *Agent) hideWork(fresh bool) {
	if fresh {
		a.dropWork()
	}
	ui := a.UI
	if a.work != nil {
		ui = a.work.ui
	}
	h, ok := ui.(Hider)
	if cols, _ := ui.Size(); !a.Cfg.HideWork || !ok || cols <= 0 {
		if a.work != nil {
			a.work.close()
			a.dropWork()
		}
		return
	}
	if a.work == nil {
		a.work = &workGroup{ui: ui, hider: h}
		a.UI = wrapWork(ui, a.work)
	}
	a.work.wait(false)
}

// dropWork takes the workUI off, the group with it, drawing nothing.
func (a *Agent) dropWork() {
	if a.work != nil {
		a.UI = a.work.ui
		a.work = nil
	}
}

// endWork ends a step of the request: the group's line is ended and the
// workUI taken off, unless the line waits for the command the shell runs.
// The workUI stays then, so that what is printed before Resume goes on
// (the proxy's word on a project no longer trusted) ends the line too.
func (a *Agent) endWork() {
	if a.work == nil || a.work.isWaiting() {
		return
	}
	a.work.close()
	a.dropWork()
}

// spinner starts the spinner of a turn: with hide_work over the group's
// line, with its label while it has calls, and the first text of the reply
// ends the group above it.
func (a *Agent) spinner(on bool) *spinner {
	if g := a.work; g != nil {
		return startSpinnerOver(g.ui, on, g.spinLabel, g.close)
	}
	return startSpinner(a.UI, on)
}

// handHidden leaves a command of the group for the shell: counted, and the
// proxy told to draw nothing of it once it runs, which it cannot before
// the request has returned.
func (a *Agent) handHidden(id, cmd string) error {
	g := a.work
	g.add(workRun)
	g.draw()
	if err := a.Shell.HandOff(id, cmd); err != nil {
		return err
	}
	g.hider.HideCommand()
	g.wait(true)
	return nil
}

// callHidden runs a call of the group, titled title: counted, nothing of it
// drawn, its result kept for Ctrl+O. A streaming tool gets no live output:
// its result has what it printed. A call that fails is shown after all, as
// it would be without hide_work, and not counted: it ends the group.
func (a *Agent) callHidden(ctx context.Context, t tools.Tool, c session.ToolCall, args map[string]any, title string) error {
	g := a.work
	k := kindOf(t)
	g.add(k)
	g.draw()
	res, err := t.Execute(ctx, a.exec, args, nil)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return ctx.Err()
		}
		g.drop(k)
		if tools.Streams(t) && strings.TrimSpace(res) != "" {
			// What it printed, behind a status, as its live output would be.
			if col := a.show(title, false); col >= 0 {
				a.UI.CommandAt(col, false, 0)
			}
			a.UI.Fold("⚙ "+title, res)
		} else {
			a.show(title, true)
		}
		fmt.Fprintf(a.UI, "%s  ✗ %v%s\n", red, err, reset)
		return a.postTool(ctx, c, args, strings.TrimSpace(res+"\n"+err.Error()), true)
	}
	g.hider.Hidden("⚙ "+title, res)
	return a.postTool(ctx, c, args, capture.Truncate(res, a.Cfg.MaxOutputBytes*4), false)
}

// leadBlanks leaves out the blank lines a reply begins with, while it has
// shown nothing else: with hide_work, blanks between calls would end the
// group's line for no text. The first line keeps its indent.
type leadBlanks struct {
	on   bool
	held string
}

func (l *leadBlanks) text(s string) string {
	if !l.on {
		return s
	}
	s = l.held + s
	rest := strings.TrimLeft(s, " \t\r\n")
	if rest == "" {
		l.held = s
		return ""
	}
	l.on, l.held = false, ""
	blank := s[:len(s)-len(rest)]
	return s[strings.LastIndexByte(blank, '\n')+1:]
}
