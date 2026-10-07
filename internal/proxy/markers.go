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

func (p *Proxy) marker(m Marker) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch m.Kind {
	case "cmd-start":
		p.dropLine()
		p.user = &segment{cmd: m.Payload, buf: capture.NewBuffer(headCap, tailCap)}
	case "ask-start":
		p.dropLine()
		p.asking = true
		p.folds = nil
		// A command that asks (an alias of __aish_ask) is the request's:
		// in the journal it would hold the whole reply as its output.
		p.user = nil
	case "cmd-end":
		defer p.drawStatus() // once the command is in the journal
		p.earlyPrompt()
		p.at = nil
		p.asking = false
		p.handed = "" // cut short by Ctrl+C or return, or never run
		if p.tool != nil {
			p.finishFold(p.tool, 130)
			p.tool = nil
		}
		// Back at the prompt: an agent command interrupted with Ctrl+C never
		// sent agent-end. Keep what it printed for the next `agent start`.
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
		rc, cwd, _ := strings.Cut(m.Payload, ";")
		defer p.saveState(cwd) // with the command that changed it in the journal
		seg := p.user
		p.user = nil
		if p.switched {
			p.switched = false
			return // `aish resume` belongs to neither session
		}
		if seg == nil || strings.TrimSpace(seg.cmd) == "" {
			return
		}
		exit, _ := strconv.Atoi(rc)
		out, tui := render(seg.buf)
		if seg.cleared && strings.TrimSpace(out) == "" {
			return // `clear` itself: nothing left on the screen
		}
		if ignoredCommand(seg.cmd, p.ignore) {
			out = session.NotRecorded
		}
		_ = p.sess.Append(session.Entry{Kind: session.KindShell, Cmd: seg.cmd, Output: out, Exit: exit, Cwd: cwd, TUI: tui})
	case "agent-start":
		id, cmd, _ := strings.Cut(m.Payload, ";")
		seg := &segment{cmd: cmd, buf: capture.NewBuffer(headCap, tailCap)}
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
	case "agent-end":
		p.stopSpin() // the agent goes on with its line
		p.stopWatch()
		f := strings.SplitN(m.Payload, ";", 3)
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
}
