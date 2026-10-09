package proxy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// prompt is a question the agent has open on the terminal, Yes or No; the
// proxy reads the answer from the keyboard, as it does the viewer's keys.
type prompt struct {
	yes  bool
	done chan string
	// shown are the lines the question takes on the screen with its
	// choices, which an interrupt erases.
	shown []string
	clock *askClock // nil: the question waits as long as its ctx
	firm  bool      // a question the agent's code may have opened, see confirm.go
	from  time.Time // when the keys of a firm one start to count
}

// choices is the block of answers drawn after the question: the one chosen
// in reverse and in brackets, which show without colours too. It is as
// wide whichever is chosen, or none, so a redraw steps back choicesWidth
// columns and draws it anew. Not with \e7 and \e8: the status of the
// prompt keeps the one saved cursor, and a scroll would move it elsewhere.
func choices(chosen string) string {
	var b strings.Builder
	for i, s := range []string{"Yes", "No"} {
		if i > 0 {
			b.WriteByte(' ')
		}
		if s == chosen {
			b.WriteString("\x1b[7m[ " + s + " ]\x1b[27m")
		} else {
			b.WriteString("  " + s + "  ")
		}
	}
	return b.String()
}

var choicesWidth = frameWidth(choices(""))

func (pr *prompt) chosen() string {
	if pr.yes {
		return "Yes"
	}
	return "No"
}

// askUser prints q with Yes and No after it and waits for the user to pick
// one, or for ctx: Ctrl+C goes to the shell, which stops the request, and
// the question goes off the screen as a form does, the echo of ^C with it.
// ctx past its deadline, or the time on ctx out (askClock), is no answer
// in time: the question is left with No, as if chosen, and the cause.
// Without a terminal there is nothing to draw the choices on.
func (p *Proxy) askUser(ctx context.Context, q string) (string, error) {
	p.mu.Lock()
	if p.size == nil {
		p.mu.Unlock()
		return "", errors.New("no terminal")
	}
	if p.ask != nil || p.form != nil {
		p.mu.Unlock()
		return "", errors.New("a question is open already")
	}
	pr := &prompt{yes: true, done: make(chan string, 1)}
	pr.firmly(ctx)
	p.ask = pr
	pr.clock = p.answerClock(ctx)
	p.syncPaste() // a paste is no answer
	// The choices end short of the last column: there the cursor would
	// stay on it, and stepping back from it would miss by one. So does the
	// echo of a Ctrl+C after them: wrapped, it would take a line the erase
	// does not count.
	cols, _ := p.size()
	sep := " "
	if frameWidth(q[strings.LastIndexByte(q, '\n')+1:])+len(sep)+choicesWidth+len("^C") >= cols {
		sep = "\r\n"
	}
	drawn := q + sep + choices(pr.chosen())
	pr.shown = strings.Split(strings.ReplaceAll(drawn, "\r\n", "\n"), "\n")
	p.emit([]byte("\x1b[?25l" + drawn))
	p.mu.Unlock()
	select {
	case ans := <-pr.done:
		return ans, nil
	case <-ctx.Done():
	case <-pr.clock.ranOut():
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	pr.clock.stop()
	err, why := pr.clock.ended(ctx)
	if p.ask != pr {
		// Answered as the question ended: the screen shows the answer, and
		// so it stands.
		select {
		case ans := <-pr.done:
			return ans, nil
		default:
			return "", err
		}
	}
	p.ask = nil
	end := ""
	if errors.Is(err, context.DeadlineExceeded) {
		// The keyboard is back as after an answer, and the line says that
		// none came.
		end = fmt.Sprintf("\x1b[%dDNo", choicesWidth)
		if why != err {
			end += " (" + why.Error() + ")"
		}
		end += "\x1b[K\r\n"
	} else {
		// Interrupted: the terminal echoed ^C as it signalled the client,
		// which only then asks to stop, so the echo is on the screen
		// already and goes with the question, as with a form. The prompt
		// starts where the question did, below the call.
		cols, _ := p.size()
		end = (&openForm{shown: pr.shown}).erase(cols)
	}
	p.emit([]byte(end + "\x1b[?25h"))
	p.syncPaste()
	return "", err
}

// askKey reads the answer to the open question from what the user typed
// and draws the choice anew: the shell is not reading, so nothing else
// would. y and n answer at once, Enter answers with the choice. Returns
// what goes on to the shell anyway. Called under p.mu.
func (p *Proxy) askKey(b []byte) []byte {
	b = p.ask.firmKeys(b)
	back := fmt.Sprintf("\x1b[%dD", choicesWidth)
	choose := func(yes bool) {
		if p.ask.yes != yes {
			p.ask.yes = yes
			p.emit([]byte(back + choices(p.ask.chosen())))
		}
	}
	var pass []byte
	for i := 0; i < len(b); i++ {
		c := b[i]
		if c == 0x1b {
			// An escape sequence: an arrow is told by its last byte, and
			// Alt with a key is no answer.
			if i++; i >= len(b) || b[i] != '[' && b[i] != 'O' {
				continue
			}
			csi := b[i] == '['
			for i++; csi && i < len(b) && (b[i] < 0x40 || b[i] > 0x7e); i++ {
			}
			if i >= len(b) {
				break
			}
			switch b[i] {
			case 'D':
				c = 'h'
			case 'C':
				c = 'l'
			case 'A', 'B', 'Z':
				c = '\t' // up, down, Shift+Tab: in one row either way is the other
			default:
				continue
			}
		}
		switch c {
		case 'h':
			choose(true)
		case 'l':
			choose(false)
		case '\t':
			choose(!p.ask.yes)
		case 'y', 'Y', 'n', 'N':
			p.ask.yes = c == 'y' || c == 'Y'
			fallthrough
		case '\r', '\n':
			// The line keeps what was chosen.
			p.emit([]byte(back + p.ask.chosen() + "\x1b[K\r\n\x1b[?25h"))
			ans := "n"
			if p.ask.yes {
				ans = "y"
			}
			p.ask.done <- ans
			p.ask.clock.stop()
			p.ask = nil
			return append(pass, b[i+1:]...)
		case 0x03:
			// Interrupts the request, like anywhere else; the question
			// goes when it ends, see askUser.
			pass = append(pass, c)
		case ctrlO:
			if folds := p.viewFolds(); len(folds) > 0 {
				p.openView(folds)
				// The time stands while the viewer covers the question.
				p.ask.clock.sync()
				return pass // the rest would be the viewer's
			}
		}
	}
	return pass
}
