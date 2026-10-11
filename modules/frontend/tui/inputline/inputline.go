// Package inputline is the line the user types in, a pane at the bottom: Enter sends it as a user.message.
// Shift+Enter, or \ before Enter as in bash, starts a new line; a paste keeps its newlines and tabs.
// The pane grows with the text, wrapped to its width. The cursor moves as in bash: Left/Right and Ctrl+B/F
// by a character, Alt+B/F and Ctrl+Left/Right by a word, Home/End and Ctrl+A/E to the line's ends.
// Backspace and Delete delete a character, Ctrl+W the word before, Alt+D the word after,
// Ctrl+U and Ctrl+K to the line's start and end.
// A message the bus rolls back comes back to the line if it's still empty.
// Ctrl+C stops the answers still running; with none, it's left to the compositor, which quits.
package inputline

import (
	"context"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/GoldenDeals/aish-core/contracts/llm"
	"github.com/GoldenDeals/aish-core/contracts/tui"
	"github.com/GoldenDeals/aish-core/pkg/bus"
	"github.com/GoldenDeals/aish-core/pkg/config"
	"github.com/GoldenDeals/aish-core/pkg/event"
	"github.com/GoldenDeals/aish-core/pkg/module"
)

const (
	z      = 10 // above the conversation for input; the higher z of a status line puts it below
	prompt = "> "
)

type mod struct {
	b      bus.Bus
	ctx    context.Context // ends on Stop, dropping a message still waiting for the bus
	cancel context.CancelFunc

	mu     sync.Mutex // the compositor draws and types while a rolled back message comes back
	handle tui.Handle
	text   string
	pos    int                                    // the cursor, a byte offset into text
	cur    uv.Position                            // where the last Draw put the cursor; the compositor asks for it right after
	turns  map[context.Context]context.CancelFunc // the turns of sent messages that aren't over yet
}

// bindings are the keys that move the cursor or delete from it, as in bash, and where in s from i.
var bindings = []struct {
	keys []string
	kill bool // delete up to there instead of moving
	to   func(s string, i int) int
}{
	{[]string{"left", "ctrl+b"}, false, back},
	{[]string{"right", "ctrl+f"}, false, forward},
	{[]string{"alt+b", "ctrl+left"}, false, backWord},
	{[]string{"alt+f", "ctrl+right"}, false, forwardWord},
	{[]string{"home", "ctrl+a"}, false, lineStart},
	{[]string{"end", "ctrl+e"}, false, lineEnd},
	{[]string{"backspace"}, true, back},
	{[]string{"delete"}, true, forward},
	{[]string{"ctrl+w"}, true, backField},
	{[]string{"alt+d"}, true, forwardWord},
	{[]string{"ctrl+u"}, true, lineStart},
	{[]string{"ctrl+k"}, true, lineEnd},
}

func init() { module.Register("tui.inputline", &mod{}) }

func (m *mod) Init(b bus.Bus, _ config.Config) error {
	m.b = b
	m.ctx, m.cancel = context.WithCancel(context.Background())
	m.turns = map[context.Context]context.CancelFunc{}
	bus.Async(b, event.ByName(tui.Ready), func(_ context.Context, ev event.Event[tui.Screen]) {
		m.edit(func() { m.handle = ev.Data().Open(tui.Pane, z, m) })
	})
	return nil
}

func (m *mod) Start(context.Context) error { return nil }

func (m *mod) Stop(context.Context) error {
	m.cancel()
	return nil
}

func (m *mod) Size(width, _ int) (int, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return width, len(rows(m.text, width))
}

// Draw shows the rows from the prompt on, or the ones up to the cursor's when the pane got fewer.
func (m *mod) Draw(scr uv.Screen, area uv.Rectangle) {
	m.mu.Lock()
	defer m.mu.Unlock()
	starts := rows(m.text, area.Dx())
	r := len(starts) - 1
	for starts[r] > m.pos {
		r--
	}
	top := max(r-area.Dy()+1, 0)
	for i := top; i < len(starts) && i-top < area.Dy(); i++ {
		end := len(m.text)
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		lead := strings.Repeat(" ", len(prompt))
		if i == 0 {
			lead = prompt
		}
		row := strings.NewReplacer("\n", "", "\t", " ").Replace(m.text[starts[i]:end])
		uv.NewStyledString(lead+row).Draw(scr, uv.Rect(0, i-top, area.Dx(), 1))
	}
	m.cur = uv.Pos(len(prompt)+width(m.text[starts[r]:m.pos]), r-top)
}

func (m *mod) Cursor() *uv.Cursor {
	m.mu.Lock()
	defer m.mu.Unlock()
	return uv.NewCursor(m.cur.X, m.cur.Y)
}

// rows are the byte offsets where text's rows start in a pane w wide. A row breaks after a newline and where
// the next grapheme wouldn't fit after the prompt with a column left for the cursor at the end of a line.
func rows(text string, w int) []int {
	w = max(w-len(prompt)-1, 1)
	starts, x := []int{0}, 0
	for i := 0; i < len(text); {
		c, cw := cluster(text[i:])
		if x > 0 && x+cw > w {
			starts, x = append(starts, i), 0
		}
		x, i = x+cw, i+len(c)
		if c == "\n" {
			starts, x = append(starts, i), 0
		}
	}
	return starts
}

func (m *mod) Input(ev uv.Event) bool {
	switch ev := ev.(type) {
	case uv.PasteEvent:
		m.edit(func() { m.insert(clean(ev.Content)) })
	case uv.KeyPressEvent:
		switch {
		case ev.MatchString("enter"):
			m.send()
		case ev.MatchString("shift+enter"):
			m.edit(func() { m.insert("\n") })
		case ev.MatchString("ctrl+c"):
			return m.stop()
		case m.bound(ev): // before text: alt+b may carry it
		case ev.Text != "":
			m.edit(func() { m.insert(clean(ev.Text)) })
		default:
			return false
		}
	default:
		return false
	}
	return true
}

// send clears the line and dispatches its text apart: the bus waits while an answer streams.
// A \ before the cursor turns into a newline instead, as in bash.
func (m *mod) send() {
	var msg string
	m.edit(func() {
		if strings.HasSuffix(m.text[:m.pos], `\`) {
			m.text = m.text[:m.pos-1] + "\n" + m.text[m.pos:] // as long: the cursor stays after it
			return
		}
		msg, m.text, m.pos = m.text, "", 0
	})
	if strings.TrimSpace(msg) == "" {
		return
	}
	ctx, stop := llm.WithTurn(m.ctx)
	turn := llm.Turn(ctx)
	m.mu.Lock()
	m.turns[turn] = stop
	m.mu.Unlock()
	context.AfterFunc(turn, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		delete(m.turns, turn)
	})
	go func() {
		if err := m.b.Dispatch(ctx, event.New(llm.UserMessage, msg)); err != nil {
			stop() // the provider never got it
			if m.ctx.Err() == nil {
				m.edit(func() {
					if m.text == "" {
						m.text, m.pos = msg, len(msg) // rolled back: give it back to retry rather than lose it
					}
				})
			}
		}
	}()
}

// stop stops the turns that aren't over and tells whether there were any.
func (m *mod) stop() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, stop := range m.turns {
		stop()
	}
	n := len(m.turns)
	clear(m.turns)
	return n > 0
}

// edit changes the line under the lock and asks for a frame.
func (m *mod) edit(f func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f()
	if m.handle != nil {
		m.handle.Redraw()
	}
}

// bound moves the cursor or deletes from it if k is one of bindings' keys.
func (m *mod) bound(k uv.KeyPressEvent) bool {
	for _, b := range bindings {
		if k.MatchString(b.keys...) {
			m.edit(func() {
				j := b.to(m.text, m.pos)
				if !b.kill {
					m.pos = j
					return
				}
				i, j := min(m.pos, j), max(m.pos, j)
				m.text, m.pos = m.text[:i]+m.text[j:], i
			})
			return true
		}
	}
	return false
}

// insert puts s at the cursor and moves the cursor after it.
func (m *mod) insert(s string) {
	m.text, m.pos = m.text[:m.pos]+s+m.text[m.pos:], m.pos+len(s)
}

// cluster is the grapheme s starts with and its width; a tab shows as one space.
func cluster(s string) (string, int) {
	c, w := ansi.FirstGraphemeCluster(s, ansi.GraphemeWidth)
	if c == "\t" {
		w = 1
	}
	return c, w
}

func width(s string) (w int) {
	for s != "" {
		c, cw := cluster(s)
		s, w = s[len(c):], w+cw
	}
	return w
}

// back is where the grapheme before i starts, forward where the one at i ends.
func back(s string, i int) int {
	j := 0
	for k := 0; k < i; {
		c, _ := cluster(s[k:])
		j, k = k, k+len(c)
	}
	return j
}

func forward(s string, i int) int {
	c, _ := cluster(s[i:])
	return i + len(c)
}

// lineStart and lineEnd are the ends of the line i is on.
func lineStart(s string, i int) int { return strings.LastIndexByte(s[:i], '\n') + 1 }

func lineEnd(s string, i int) int {
	if j := strings.IndexByte(s[i:], '\n'); j >= 0 {
		return i + j
	}
	return len(s)
}

// backWord is where the word before i starts, forwardWord where the one after i ends;
// a word is letters and digits, as in bash. backField is backWord for ctrl+w, to which a word is anything but spaces.
func backWord(s string, i int) int { return backTo(s, i, isWord) }
func backField(s string, i int) int {
	return backTo(s, i, func(r rune) bool { return !unicode.IsSpace(r) })
}

// backTo is where the run of runes in before i starts, past the runes not in it right before i.
func backTo(s string, i int, in func(rune) bool) int {
	i = strings.LastIndexFunc(s[:i], in)
	i = strings.LastIndexFunc(s[:max(i, 0)], func(r rune) bool { return !in(r) })
	if i < 0 {
		return 0
	}
	_, n := utf8.DecodeRuneInString(s[i:])
	return i + n
}

func forwardWord(s string, i int) int {
	j := strings.IndexFunc(s[i:], isWord)
	if j < 0 {
		return len(s)
	}
	i += j
	if j = strings.IndexFunc(s[i:], notWord); j < 0 {
		return len(s)
	}
	return i + j
}

// isWord counts marks in so that a word never ends inside a letter with an accent.
func isWord(r rune) bool  { return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) }
func notWord(r rune) bool { return !isWord(r) }

// clean keeps printable text, newlines and tabs: a paste brings escape sequences, and newlines as \r, as terminals send them.
func clean(s string) string {
	s = strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(s)
	return strings.Map(func(r rune) rune {
		if r != '\n' && r != '\t' && unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}
