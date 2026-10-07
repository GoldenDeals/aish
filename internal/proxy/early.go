package proxy

import (
	"bytes"
	"io"
	"time"
)

// early holds the keys typed before the shell's first prompt. They would
// meet a PTY in cooked mode, ~/.bashrc running: the kernel echoes them,
// then readline prints them again after its prompt, and the echo stays
// above the request, in tmux's history too; a paste bracketed by a
// terminal that had bracketed paste on comes out as ^[[200~...^[[201~.
// Held till readline has the terminal, they are shown once, by readline,
// and a paste is a paste. Readline turns bracketed paste on right after it
// takes the terminal raw; one that does not gets the keys earlyWait after
// the first prompt.
//
// The keys go at once with a key that sends a signal, not pasted, for a
// ~/.bashrc that hangs, and when the shell prints anything before its
// first prompt: ~/.bashrc may be asking (ssh-add, read -p).
type early struct {
	w        io.Writer // the PTY
	held     []byte
	prompted bool   // cmd-end came: readline is next
	tail     []byte // the end of the output since, for pasteOn cut in two
}

// pasteOn is what readline prints to turn bracketed paste on.
var pasteOn = []byte("\x1b[?2004h")

// earlyWait is how long after the first prompt a readline that does not
// turn bracketed paste on is taken to have the terminal.
var earlyWait = 500 * time.Millisecond

// maxEarly bounds what is held: the release is written under p.mu, and
// cooked mode keeps no longer line anyway.
const maxEarly = 4 << 10

// holdEarly starts holding the keys meant for the PTY w, before the shell
// can have printed anything.
func (p *Proxy) holdEarly(w io.Writer) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.early = &early{w: w}
}

// earlyKey holds b, or returns it after what is held when it has to go.
func (p *Proxy) earlyKey(b []byte) []byte {
	e := p.early
	e.held = append(e.held, b...)
	// Ctrl+C, Ctrl+Z or Ctrl+\ inside a paste is the text pasted.
	if len(e.held) <= maxEarly && p.seq.shell.find(b, 0x03, 0x1a, 0x1c) < 0 {
		return nil
	}
	p.early = nil
	return e.held
}

// earlyPrompt takes the first cmd-end: readline is next, after the rest
// of PROMPT_COMMAND.
func (p *Proxy) earlyPrompt() {
	e := p.early
	if e == nil || e.prompted {
		return
	}
	e.prompted = true
	time.AfterFunc(earlyWait, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.early == e {
			p.releaseEarly()
		}
	})
}

// earlyOutput takes the shell's output, looking for readline's taking the
// terminal, or for ~/.bashrc speaking.
func (p *Proxy) earlyOutput(b []byte) {
	e := p.early
	if e == nil {
		return
	}
	if e.prompted {
		e.tail = append(e.tail, b...)
		if !bytes.Contains(e.tail, pasteOn) {
			e.tail = e.tail[max(len(e.tail)-len(pasteOn)+1, 0):]
			return
		}
	}
	p.releaseEarly()
}

func (p *Proxy) releaseEarly() {
	e := p.early
	p.early = nil
	if len(e.held) > 0 {
		_, _ = e.w.Write(e.held)
		if p.line != nil {
			p.line.typed() // they are the first keys at the prompt, as in key
		}
	}
}
