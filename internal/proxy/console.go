package proxy

import (
	"bytes"
	"io"
	"time"
)

// console is the user's terminal. All the proxy writes there goes to out
// through emit, which holds it while the viewer or the panes have the
// screen, or through write, which does not: the frames of the viewer and
// the panes, and the modes the keys are read in. The keys come in through
// key, under p.mu, and go on to the shell or to whoever reads them
// meanwhile: early before the first prompt, then the panes, the viewer, a
// question, the form. out and size are set before Run reads anything (the
// tests set them), size nil meaning no terminal; the rest is under p.mu.
type console struct {
	out  io.Writer
	size func() (w, h int)

	screen Screen     // whether the main screen was erased, or the alternate one is on
	line   *inputLine // the line typed at the prompt, kept off its status
	col    firstCol   // whether the shell's output left the next prompt off the first column
	view   *viewer    // open while Ctrl+O shows the folds
	panes  *panes     // open while subagents run, see panes.go
	held   []byte     // output that arrived while the viewer or the panes had the screen
	ask    *prompt    // a question the agent waits for the user to answer
	form   *openForm  // the questions of ask_user while the user answers them
	early  *early     // keys typed before readline has the terminal, see early.go
	seq    keySeq     // keys a read cut, and pastes, see pastebrackets.go
	esc    escKey     // a lone Esc during a request, see esc.go
	lines  lineMode   // whether the shell's terminal reads lines (esc.go); nil without one
}

// emit writes to the terminal, or holds the output while the viewer or the
// panes have the screen.
func (t *console) emit(b []byte) {
	if t.holding() {
		t.held = append(t.held, b...)
		return
	}
	t.write(b)
}

// write writes to the terminal at once, past what the viewer and the panes
// hold: what they draw on the screen they have, and the modes the keys are
// read in.
func (t *console) write(b []byte) { _, _ = t.out.Write(b) }

// openView opens the viewer of folds on the alternate screen. It shows
// what comes while it is open, see viewFrame. Called under p.mu.
func (p *Proxy) openView(folds []Fold) {
	w, h := p.size()
	v := newViewer(folds, w, h)
	p.view = v
	p.write(v.open())
	v.timer = time.AfterFunc(viewTick, func() { p.viewFrame(v) })
}

// input copies the keyboard to bash. Ctrl+O while the assistant works or
// at the prompt toggles the viewer of folded outputs instead.
func (p *Proxy) input(r io.Reader, w io.Writer) {
	buf := make([]byte, 4<<10)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if b := p.key(buf[:n]); len(b) > 0 {
				if _, err := w.Write(b); err != nil {
					return
				}
			}
		}
		if err != nil {
			return
		}
	}
}

const ctrlO = 0x0f

// key handles the viewer and returns the input meant for bash.
func (p *Proxy) key(b []byte) []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.early != nil {
		return p.earlyKey(b)
	}
	defer p.syncPaste() // the keys may have opened or closed a reader of them
	keys := p.takeKeys(p.shiftEnter(p.wholeKeys(b)))
	p.typedHidden(keys) // a hidden command they go to, see hidework.go
	return keys
}

// takeKeys is key for the keys wholeKeys gave whole. Called under p.mu.
func (p *Proxy) takeKeys(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	if p.panes != nil && p.panes.shown {
		return p.paneKey(b)
	}
	if p.view != nil {
		if p.view.key(b) {
			p.closeView()
		} else {
			p.write(p.view.render())
		}
		return nil
	}
	if p.ask != nil {
		return p.askKey(b)
	}
	if p.line != nil {
		p.line.typed()
		if bytes.ContainsAny(b, "\r\n") {
			if s := p.line.submit(); s != nil {
				p.emit(s)
			}
		}
	}
	if p.form != nil {
		return p.formKey(b)
	}
	if b = p.escKeys(b); len(b) == 0 {
		return nil
	}
	// A user's command (an editor, say) gets Ctrl+O as usual, and readline
	// one pasted, with the rest of the paste.
	i := p.seq.shell.find(b, ctrlO)
	if i < 0 || p.user != nil {
		return b
	}
	if p.panes != nil {
		p.showPanes() // the subagents' layout, left with q, comes back
		return b[:i]
	}
	folds := p.viewFolds()
	if len(folds) == 0 {
		if p.asking {
			return append(b[:i:i], b[i+1:]...) // would only be echoed as ^O
		}
		return b // readline's own Ctrl+O
	}
	p.openView(folds)
	return b[:i]
}

func (p *Proxy) resized() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dropLine() // the terminal rewrapped the lines the model knows
	if p.view != nil {
		p.view.resize(p.size())
		p.write(append([]byte("\x1b[2J"), p.view.render()...))
	}
	if p.panes != nil && p.panes.shown {
		p.panes.resize(p.size())
		p.drawPanes() // whole: it covers the screen
	}
	if p.form != nil {
		p.drawForm() // held while the viewer is open
	}
}

// restoreScreen takes the viewer, the panes and the form off the terminal
// once the shell is gone: nobody would close them, and the terminal would
// be left on the alternate screen or without its cursor, or in the
// bracketed paste mode the proxy set. The form ends unanswered, as on Esc.
func (p *Proxy) restoreScreen() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.view != nil {
		// With what it held: the form's last frame, which closeForm erases.
		p.closeView()
	}
	if p.panes != nil {
		p.closePanes()
	}
	if p.form != nil {
		p.closeForm()
	}
	// The shell may have gone with the agent's command still running.
	p.stopSpin()
	p.stopWatch()
	p.setPaste(false) // even under a question, which waits for its request
	p.atPrompt(false) // modifyOtherKeys too, after a Ctrl+D at the prompt
}
