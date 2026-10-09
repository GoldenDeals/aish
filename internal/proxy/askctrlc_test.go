package proxy

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

// vt is enough of a terminal cols wide for a question: text wraps at the
// right edge as in xterm, the cursor waiting there for the next letter;
// \r, \n and the CSI sequences that move the cursor (A, B, D) or erase
// below it (K, J) do what they do there; other sequences draw nothing.
type vt struct {
	cols     int
	rows     [][]rune
	row, col int
	wrap     bool
}

func newVT(cols int) *vt { return &vt{cols: cols, rows: [][]rune{nil}} }

func (v *vt) play(s string) {
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		switch c := rs[i]; {
		case c == '\r':
			v.col, v.wrap = 0, false
		case c == '\n':
			v.down(1)
		case c == 0x1b && i+1 < len(rs) && rs[i+1] == '[':
			j := i + 2
			for j < len(rs) && (rs[j] < 0x40 || rs[j] > 0x7e) {
				j++
			}
			if j == len(rs) {
				return
			}
			v.csi(string(rs[i+2:j]), rs[j])
			i = j
		case c >= 0x20:
			v.put(c)
		}
	}
}

func (v *vt) csi(arg string, final rune) {
	n, err := strconv.Atoi(arg)
	if err != nil || n < 1 {
		n = 1
	}
	if strings.HasPrefix(arg, "?") {
		return // modes: the cursor shown or hidden, bracketed paste
	}
	switch final {
	case 'A':
		v.row, v.wrap = max(v.row-n, 0), false
	case 'B':
		v.row, v.wrap = min(v.row+n, len(v.rows)-1), false
	case 'D':
		v.col, v.wrap = max(v.col-n, 0), false
	case 'K':
		v.cut()
	case 'J':
		v.cut()
		v.rows = v.rows[:v.row+1]
	}
}

func (v *vt) down(n int) {
	v.wrap = false
	for ; n > 0; n-- {
		if v.row++; v.row == len(v.rows) {
			v.rows = append(v.rows, nil)
		}
	}
}

func (v *vt) cut() {
	if v.col < len(v.rows[v.row]) {
		v.rows[v.row] = v.rows[v.row][:v.col]
	}
}

func (v *vt) put(c rune) {
	if v.wrap {
		v.down(1)
		v.col = 0
	}
	r := v.rows[v.row]
	for len(r) <= v.col {
		r = append(r, ' ')
	}
	r[v.col] = c
	v.rows[v.row] = r
	if v.col == v.cols-1 {
		v.wrap = true
	} else {
		v.col++
	}
}

// lines are the rows of the screen down to the cursor's and the last one
// with text, the trailing spaces cut.
func (v *vt) lines() []string {
	var out []string
	for _, r := range v.rows {
		out = append(out, strings.TrimRight(string(r), " "))
	}
	for len(out) > v.row+1 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// Ctrl+C in a question goes to the shell and draws nothing; the request it
// stops takes the question off the screen, however many lines its reason
// wraps to, and the echo of ^C after the choices with it. The prompt then
// starts at the left edge of the line below the call, as after Ctrl+C in a
// form.
func TestAskCtrlC(t *testing.T) {
	const call = "\x1b[36m❯\x1b[39m \x1b[1mgit push --force origin main\x1b[0m\r\n"
	reason := "force push rewrites the remote history of a shared branch; everyone who pulled it will have to rebase"
	for _, tc := range []struct {
		name string
		cols int
		q    string
	}{
		{"reason over three lines", 40, reason + " - allow?"},
		{"reason over two lines", 110, reason + " - allow?"},
		{"choices after the question", 80, "allow?"},
		// Choices that would end a column short of the edge without the
		// room for the echo: the C of ^C would wrap below them.
		{"choices to the edge", 40, strings.Repeat("x", 40-16)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, out := termProxy(t)
			p.size = func() (int, int) { return tc.cols, 24 }
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			res := make(chan error, 1)
			go func() {
				_, err := p.askUser(ctx, "\x1b[1m"+tc.q+"\x1b[0m")
				res <- err
			}()
			for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
				p.mu.Lock()
				open := p.ask != nil
				p.mu.Unlock()
				if open {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("the question never opened")
				}
			}
			drawn := out.String()
			if got := p.key([]byte{0x03}); string(got) != "\x03" {
				t.Errorf("Ctrl+C did not reach the shell: %q", got)
			}
			if s := out.String(); s != drawn {
				t.Errorf("Ctrl+C drew %q", strings.TrimPrefix(s, drawn))
			}
			// The terminal echoes Ctrl+C at once; the client asks to stop
			// after that.
			p.mu.Lock()
			p.emit([]byte("^C"))
			p.mu.Unlock()
			cancel()
			select {
			case err := <-res:
				if !errors.Is(err, context.Canceled) {
					t.Errorf("interrupted question: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the question stayed after its request")
			}
			if p.ask != nil {
				t.Error("the question stayed open")
			}

			v := newVT(tc.cols)
			v.play(call + out.String())
			if got := v.lines(); len(got) != 2 || got[0] != "❯ git push --force origin main" || got[1] != "" {
				t.Errorf("screen after Ctrl+C:\n%s", strings.Join(got, "\n"))
			}
			if v.row != 1 || v.col != 0 {
				t.Errorf("the prompt starts at row %d, column %d, not below the call", v.row, v.col)
			}
			after := strings.TrimPrefix(out.String(), drawn+"^C")
			if strings.Contains(after, "Yes") || strings.Contains(after, "No") {
				t.Errorf("the choices drawn after the erase: %q", after)
			}
			if !strings.HasSuffix(modeless(out.String()), "\x1b[?25h") {
				t.Errorf("the cursor stayed hidden: %q", out.String())
			}
			if got := p.key([]byte("ls")); string(got) != "ls" {
				t.Errorf("the keys after it: %q", got)
			}
		})
	}
}
