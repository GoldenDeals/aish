package proxy

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/GoldenDeals/aish/internal/capture"
	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/session"
)

// maxMarker bounds how much output an unterminated marker holds back. Not
// smaller: cmd-start carries the whole command line, pasted ones included.
const maxMarker = 64 << 10

// Marker is one decoded aish OSC sequence.
type Marker struct {
	Kind    string
	Payload string
}

// Filter removes aish markers from a byte stream. Markers may be split
// across reads, so incomplete tails are held back until the next call.
type Filter struct {
	prefix  []byte // OSC 6973 ; <nonce> ;
	pending []byte
}

// NewFilter takes the markers OSC 6973 ; <nonce> ; <kind> ; <payload> BEL.
// The nonce keeps a marker inside a file being cat'ed or a program's output
// from passing for the shell's own: with any other nonce it stays text.
func NewFilter(nonce string) *Filter {
	return &Filter{prefix: []byte("\x1b]6973;" + nonce + ";")}
}

// Feed returns the bytes to pass through and the markers found, in order.
// Each marker is reported together with the passthrough bytes preceding it,
// so callers can attribute output to the right command.
func (f *Filter) Feed(p []byte, onText func([]byte), onMarker func(Marker)) {
	data := p
	if len(f.pending) > 0 {
		data = append(f.pending, p...)
		f.pending = nil
	}
	for len(data) > 0 {
		i := bytes.IndexByte(data, 0x1b)
		if i < 0 {
			onText(data)
			return
		}
		rest := data[i:]
		if len(rest) < len(f.prefix) {
			if bytes.HasPrefix(f.prefix, rest) {
				if i > 0 {
					onText(data[:i])
				}
				f.pending = append([]byte{}, rest...)
				return
			}
		} else if bytes.HasPrefix(rest, f.prefix) {
			end := bytes.IndexAny(rest[len(f.prefix):], "\a\x1b")
			if end >= 0 && rest[len(f.prefix)+end] == 0x1b {
				// A payload holds no ESC: our marker was cut short (its
				// printer killed), and the output goes on from the ESC.
				if i > 0 {
					onText(data[:i])
				}
				data = rest[len(f.prefix)+end:]
				continue
			}
			if end < 0 {
				if len(rest) > maxMarker {
					// Not a real marker; give up and pass it through.
					onText(data)
					return
				}
				if i > 0 {
					onText(data[:i])
				}
				f.pending = append([]byte{}, rest...)
				return
			}
			if i > 0 {
				onText(data[:i])
			}
			end += len(f.prefix)
			onMarker(parseMarker(rest[len(f.prefix):end]))
			data = rest[end+1:]
			continue
		}
		onText(data[:i+1])
		data = data[i+1:]
	}
}

func parseMarker(b []byte) Marker {
	kind, payload, _ := bytes.Cut(b, []byte{';'})
	return Marker{Kind: string(kind), Payload: string(payload)}
}

// marker takes a marker of the shell's, the output before it already
// recorded (Filter.Feed): the recorder starts and ends the segment the
// output goes to next. The payloads are as the header of init.bash has
// them.
func (p *Proxy) marker(m Marker) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch m.Kind {
	case "cmd-start":
		p.cmdStart(m.Payload)
	case "ask-start":
		p.askStart()
	case "cmd-end":
		p.cmdEnd(m.Payload)
	case "agent-start":
		p.agentCmdStart(m.Payload)
	case "agent-end":
		p.agentCmdEnd(m.Payload)
	}
	p.promptMarker(m.Kind) // Shift+Enter there, see shiftenter.go
}

// cmdStart begins the command line cmd the user typed. Called under p.mu.
func (p *Proxy) cmdStart(cmd string) {
	p.dropLine()
	p.dropEdits() // neovim's diffs from before it are no one's
	p.user = &segment{cmd: cmd, buf: capture.NewText(headCap, tailCap)}
}

// askStart begins a request. Called under p.mu.
func (p *Proxy) askStart() {
	p.dropLine()
	p.asking = true
	p.folds = nil
	// A command that asks (an alias of __aish_ask) is the request's:
	// in the journal it would hold the whole reply as its output.
	p.user = nil
}

// cmdEnd is the shell back at its prompt, payload "rc;cwd": what the
// command line left open ends, the user's command goes to the journal,
// then the shell's state, which __aish_precmd has just dumped, is saved
// with it, and the status is drawn. Called under p.mu.
func (p *Proxy) cmdEnd(payload string) {
	p.earlyPrompt()
	p.at = nil
	p.asking = false
	p.handed = "" // cut short by Ctrl+C or return, or never run
	p.closeTool()
	p.closeInterrupted()
	rc, cwd, _ := strings.Cut(payload, ";")
	p.recordUser(rc, cwd)
	p.saveState(cwd) // with the command that changed it in the journal
	p.drawStatus()   // once the command is in the journal
}

// closeTool ends the live output of an external tool the prompt cut short.
// Called under p.mu.
func (p *Proxy) closeTool() {
	if p.tool != nil {
		p.finishFold(p.tool, 130)
		p.tool = nil
	}
}

// closeInterrupted ends the agent's commands the prompt cut short: one
// interrupted with Ctrl+C never sent agent-end. What it printed is kept
// for the next `agent start`, which closes its call. Called under p.mu.
func (p *Proxy) closeInterrupted() {
	for id, seg := range p.agent {
		out, tui := render(seg.buf)
		p.finish(id, rpc.Output{Output: out, Exit: 130, TUI: tui})
		if seg.fold != nil {
			p.finishFold(seg.fold, 130)
		} else if seg.last.text {
			p.emit([]byte("\r\n"))
		}
	}
	clear(p.agent)
	if p.waits {
		// The agent's line of calls waited for a command cut short, or
		// never run: no agent goes on with it now, the prompt would.
		p.emit([]byte("  " + dim + "(interrupted)" + reset + "\r\n"))
	}
	p.hide, p.waits = false, false
	p.stopSpin()
	p.stopWatch()
}

// recordUser puts the command the user typed in the journal, with its
// output, its exit code rc and cwd, the directory it left the shell in.
// Called under p.mu.
func (p *Proxy) recordUser(rc, cwd string) {
	seg := p.user
	p.user = nil
	if p.switched {
		p.switched = false
		return // `aish resume` belongs to neither session
	}
	if seg == nil || strings.TrimSpace(seg.cmd) == "" {
		return
	}
	if aishOnly(seg.cmd) {
		return // `aish status` and the like: the session told of itself
	}
	exit, _ := strconv.Atoi(rc)
	out, tui := render(seg.buf)
	out, tui = p.withEdits(out, tui) // what it saved in neovim, editdiff.go
	if seg.cleared && strings.TrimSpace(out) == "" {
		return // `clear` itself: nothing left on the screen
	}
	if ignoredCommand(seg.cmd, p.ignore) {
		out = session.NotRecorded
	}
	_ = p.sess.Append(session.Entry{Kind: session.KindShell, Cmd: seg.cmd, Output: out, Exit: exit, Cwd: cwd, TUI: tui})
}

// agentCmdStart begins the agent's command the shell runs, payload
// "id;cmd": its output is folded as the agent left it to be. Called under
// p.mu.
func (p *Proxy) agentCmdStart(payload string) {
	id, cmd, _ := strings.Cut(payload, ";")
	seg := &segment{cmd: cmd, buf: capture.NewText(headCap, tailCap)}
	if p.hide {
		seg.fold = newQuiet("❯ " + cmd)
		if p.spin != nil {
			p.spin.fold = seg.fold
		}
	} else if p.foldLines >= 0 {
		seg.fold = newFold("❯ "+cmd, p.foldLines)
		if p.foldLines == 0 {
			seg.fold.at = p.at
		}
		p.watchFold(seg.fold) // it may wait for input, see foldprompt.go
	}
	p.at, p.hide = nil, false
	p.agent[id] = seg
}

// agentCmdEnd ends the agent's command, payload "id;rc;cwd", and gives
// its output to the agent waiting for it. Called under p.mu.
func (p *Proxy) agentCmdEnd(payload string) {
	p.stopSpin() // the agent goes on with its line
	p.stopWatch()
	f := strings.SplitN(payload, ";", 3)
	if len(f) < 3 {
		return
	}
	id := f[0]
	seg, ok := p.agent[id]
	if !ok {
		return
	}
	delete(p.agent, id)
	exit, _ := strconv.Atoi(f[1])
	if seg.fold != nil {
		p.finishFold(seg.fold, exit)
	} else if seg.last.text {
		p.emit([]byte("\r\n"))
	}
	out, tui := render(seg.buf)
	p.finish(id, rpc.Output{Output: out, Exit: exit, Cwd: f[2], TUI: tui})
}
