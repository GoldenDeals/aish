package proxy

import (
	"bytes"
	"io"
)

// emit writes to the terminal, or holds the output while the viewer or the
// panes have the screen.
func (p *Proxy) emit(b []byte) {
	if p.holding() {
		p.held = append(p.held, b...)
		return
	}
	_, _ = p.out.Write(b)
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
	keys := p.takeKeys(p.wholeKeys(b))
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
			_, _ = p.out.Write(p.view.render())
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
	w, h := p.size()
	p.view = newViewer(folds, w, h)
	_, _ = p.out.Write(p.view.open())
	return b[:i]
}

func (p *Proxy) resized() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dropLine() // the terminal rewrapped the lines the model knows
	if p.view != nil {
		p.view.resize(p.size())
		_, _ = p.out.Write(append([]byte("\x1b[2J"), p.view.render()...))
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
}
