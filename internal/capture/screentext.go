package capture

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
)

// CleanScreen is Clean on a terminal cols columns wide: the text a stream
// leaves on its screen and above it, one line of text for each line
// printed, however many rows the terminal wrapped it in. Redrawing that
// crosses rows (a line longer than the terminal edited in readline or zle,
// a completion list drawn below the prompt, a progress bar of several
// lines) is drawn over as on the terminal, where Clean would leave pieces
// of each frame: the cursor goes up and down the rows printed, never above
// the first, the screen's top being unknown, and \e[J erases the rows
// below it. Positions on the screen (\e[H) are not followed, and neither
// is erasing all of it, as by Clean. A stream that begins mid-line or
// mid-screen, the tail of a Buffer, starts at the first column of the
// first row. cols <= 0 is a terminal of any width: nothing wraps.
func CleanScreen(raw []byte, cols int) string {
	g := &grid{cols: max(cols, 0), rows: make([]row, 1)}
	g.feed(raw)
	return strings.TrimRight(strings.Join(append(g.done, g.lines()...), "\n"), "\n")
}

// Screen is CleanScreen of the bytes kept. The tail after the bytes
// dropped begins a screen of its own: whatever it began in the middle of,
// what it redraws stays below the omission.
func (b *Buffer) Screen(cols int) string {
	if b.dropped == 0 {
		return CleanScreen(b.Bytes(), cols)
	}
	tail := append(append([]byte{}, b.tail[b.tailPos:]...), b.tail[:b.tailPos]...)
	if b.alt {
		tail = append(tail, altScreenSeqs[0]...)
	} else {
		tail = append(tail, b.held...)
	}
	return fmt.Sprintf("%s\n[... %d bytes omitted ...]\n%s",
		CleanScreen(b.head, cols), b.dropped, CleanScreen(tail, cols))
}

const (
	// maxCell is how long a cell's string grows with combining marks.
	maxCell = 64
	// screenRows is the most rows a screen has: how far down the rows
	// inserted and deleted at the cursor move those below.
	screenRows = 256
)

// cell is one column of a row. A wide character takes two, a tab past the
// end of the row up to eight: the first holds it, the rest are cont.
type cell struct {
	s    string // the character with its combining marks, a tab, or "" for a blank
	cont bool   // taken by the wide character or the tab before it
}

type row struct {
	cells   []cell
	wrapped bool // the line goes on in the next row: the terminal wrapped it there
}

// grid is the terminal CleanScreen draws on: the rows printed since the
// last full-screen program, whose line and those above it are done.
type grid struct {
	cols   int
	done   []string
	rows   []row
	r, c   int  // the cursor; c == cols is past the last column, where the next character wraps
	noWrap bool // \e[?7l: the last column is overwritten instead
	saved  struct {
		r, c int
		ok   bool
	}
}

func (g *grid) feed(raw []byte) {
	for i := 0; i < len(raw); {
		c := raw[i]
		switch {
		case c == 0x1b:
			n := escapeLen(raw[i:])
			if isScreenOn(raw[i : i+n]) {
				i += n + g.fullScreen(raw[i+n:])
				continue
			}
			g.escape(raw[i : i+n])
			i += n
			continue
		case c == '\n':
			g.down()
			g.c = 0 // the PTY sends \r\n; a bare \n is a line all the same, as for Clean
		case c == '\r':
			g.c = 0
		case c == '\b':
			g.left(1)
		case c == '\t':
			g.tab()
		case c < 0x20 || c == 0x7f:
			// other control characters are invisible
		default:
			r, size := utf8.DecodeRune(raw[i:])
			g.print(r)
			i += size
			continue
		}
		i++
	}
}

func (g *grid) escape(seq []byte) {
	if len(seq) == 2 {
		switch seq[1] {
		case '7': // DECSC
			g.saved.r, g.saved.c, g.saved.ok = g.r, g.c, true
		case '8': // DECRC
			g.restore()
		case 'M': // reverse index
			g.up(1)
		case 'D': // index
			g.down()
		case 'E': // next line
			g.down()
			g.c = 0
		}
		return
	}
	switch string(seq) {
	case "\x1b[?7l":
		g.noWrap = true
		g.c = min(g.c, g.limit())
		return
	case "\x1b[?7h":
		g.noWrap = false
		return
	case "\x1b[s":
		g.saved.r, g.saved.c, g.saved.ok = g.r, g.c, true
		return
	case "\x1b[u":
		g.restore()
		return
	}
	final, arg, ok := csi(seq)
	if !ok {
		return // colors, titles, modes
	}
	n := max(arg, 1)
	switch final {
	case 'A':
		g.up(n)
	case 'B':
		g.downBy(n)
	case 'E':
		g.downBy(n)
		g.c = 0
	case 'F':
		g.up(n)
		g.c = 0
	case 'C':
		g.right(n)
	case 'D':
		g.left(n)
	case 'G', '`':
		g.column(n - 1)
	case 'K':
		g.eraseLine(arg)
	case 'J':
		if arg == 0 { // below; above and all of it are rows of a screen whose top is unknown
			g.eraseLine(0)
			g.rows = g.rows[:g.r+1]
		}
	case '@':
		g.insert(n)
	case 'P':
		g.delete(n)
	case 'X':
		g.erase(n)
	case 'L':
		g.insertRows(n)
	case 'M':
		g.deleteRows(n)
	}
}

// fullScreen takes the stretch on the alternate screen that rest begins
// and returns its length. It is a line, FullScreen, below the rows printed,
// which are done: the cursor comes back below that line, and nothing drawn
// after goes over them. Programs nothing on the main screen came between
// share their line, as for Clean.
func (g *grid) fullScreen(rest []byte) int {
	lines := g.lines()
	if n := len(lines); n > 0 && lines[n-1] == "" && g.r == len(g.rows)-1 {
		lines = lines[:n-1] // the line the cursor is on, nothing in it yet
	}
	g.done = append(g.done, lines...)
	k := len(g.done) - 1
	for k >= 0 && g.done[k] == "" {
		k--
	}
	if k < 0 || g.done[k] != FullScreen {
		g.done = append(g.done, FullScreen)
	}
	g.rows = append(g.rows[:0], row{})
	g.r, g.c, g.saved.ok = 0, 0, false
	j, n := firstOf(rest, screenOff)
	if j < 0 {
		return len(rest) // never left
	}
	return j + n
}

func (g *grid) row() *row { return &g.rows[g.r] }

// limit is the column farthest right the cursor is moved to: the last one,
// or farCol past the end of the row on a terminal of any width.
func (g *grid) limit() int {
	if g.cols > 0 {
		return g.cols - 1
	}
	return len(g.row().cells) + farCol
}

func (g *grid) print(r rune) {
	if r >= 0x80 && r < 0xa0 {
		return // C1 controls
	}
	w := runewidth.RuneWidth(r)
	if w == 0 {
		g.combine(r)
		return
	}
	if g.cols > 0 {
		w = min(w, g.cols)
		if g.c+w > g.cols {
			if g.noWrap {
				g.c = g.cols - w
			} else {
				g.rows[g.r].wrapped = true
				g.down()
				g.c = 0
			}
		}
	}
	rw := g.row()
	rw.split(g.c)
	rw.split(g.c + w)
	rw.pad(g.c + w)
	rw.cells[g.c] = cell{s: string(r)}
	for i := 1; i < w; i++ {
		rw.cells[g.c+i] = cell{cont: true}
	}
	g.c += w
	if g.noWrap {
		g.c = min(g.c, g.limit())
	}
}

// combine adds a mark of no width to the character before the cursor, up
// to maxCell bytes of them: a stream of marks alone is not grown in one
// string over and over.
func (g *grid) combine(r rune) {
	rw := g.row()
	i := g.c - 1
	if i < 0 || i >= len(rw.cells) {
		return
	}
	for i > 0 && rw.cells[i].cont {
		i--
	}
	if s := rw.cells[i].s; s != "" && s != "\t" && len(s) < maxCell {
		rw.cells[i].s += string(r)
	}
}

// tab moves to the next tab stop. Past the end of the row it leaves a tab
// in the text rather than the blanks it shows.
func (g *grid) tab() {
	next := min((g.c/8+1)*8, g.limit())
	if next <= g.c {
		return
	}
	if rw := g.row(); g.c >= len(rw.cells) {
		rw.pad(g.c)
		rw.cells = append(rw.cells, cell{s: "\t"})
		for range next - g.c - 1 {
			rw.cells = append(rw.cells, cell{cont: true})
		}
	}
	g.c = next
}

// down goes to the next row, a new one below the last: the terminal
// scrolls there.
func (g *grid) down() {
	if g.r == len(g.rows)-1 {
		g.rows = append(g.rows, row{})
	}
	g.r++
}

// up, downBy and right bring the cursor back from past the last column,
// as tmux does.
func (g *grid) up(n int) {
	g.r = max(g.r-n, 0)
	g.fit()
}

func (g *grid) downBy(n int) {
	g.r = min(g.r+n, len(g.rows)-1)
	g.fit()
}

func (g *grid) right(n int) {
	g.c = min(g.c+n, max(g.c, g.limit())) // on a terminal of any width never back, as for Clean
	g.fit()
}

func (g *grid) fit() {
	if g.cols > 0 {
		g.c = min(g.c, g.cols-1)
	}
}

func (g *grid) left(n int) { g.c = max(g.c-n, 0) }

func (g *grid) column(c int) { g.c = max(min(c, g.limit()), 0) }

func (g *grid) restore() {
	if g.saved.ok {
		g.r = min(g.saved.r, len(g.rows)-1)
		g.column(g.saved.c)
	}
}

// eraseLine is \e[K: to the end of the row (0), from its start through the
// cursor (1), all of it (2). A row erased to its end no longer wraps, as
// in xterm, and the line in the row above no longer goes on in a row
// erased whole, as in tmux: so readline leaves the rows of a longer line
// it redrew.
func (g *grid) eraseLine(how int) {
	rw := g.row()
	switch how {
	case 0:
		rw.split(g.c)
		rw.cells = rw.cells[:min(g.c, len(rw.cells))]
		rw.wrapped = false
		if g.c == 0 {
			g.unwrapAbove()
		}
	case 1:
		if g.c+1 >= len(rw.cells) {
			rw.cells = rw.cells[:0] // no blanks for the next row to go on after
			break
		}
		rw.split(g.c + 1)
		for i := 0; i <= g.c; i++ {
			rw.cells[i] = cell{}
		}
	case 2:
		rw.cells = rw.cells[:0]
		rw.wrapped = false
		g.unwrapAbove()
	}
}

// insert is \e[@: blanks at the cursor push the rest of the row right;
// past the last column it is gone.
func (g *grid) insert(n int) {
	rw := g.row()
	if g.c >= len(rw.cells) {
		return
	}
	rw.split(g.c)
	rw.cells = slices.Insert(rw.cells, g.c, make([]cell, min(n, max(g.cols, farCol)))...)
	if g.cols > 0 && len(rw.cells) > g.cols {
		rw.split(g.cols)
		rw.cells = rw.cells[:g.cols]
	}
}

// delete is \e[P: the rest of the row comes left over the cells deleted,
// blanks come in at its end.
func (g *grid) delete(n int) {
	rw := g.row()
	if g.c >= len(rw.cells) {
		return
	}
	end := min(g.c+n, len(rw.cells))
	rw.split(g.c)
	rw.split(end)
	rw.cells = append(slices.Delete(rw.cells, g.c, end), make([]cell, end-g.c)...)
}

// erase is \e[X: blanks from the cursor on, which stays.
func (g *grid) erase(n int) {
	rw := g.row()
	end := min(g.c+n, len(rw.cells))
	rw.split(g.c)
	rw.split(end)
	for i := g.c; i < end; i++ {
		rw.cells[i] = cell{}
	}
}

// insertRows is \e[L: blank rows at the cursor's push it and those below
// down to the bottom of the screen, the last row or screenRows below the
// cursor: those pushed past it are gone.
func (g *grid) insertRows(n int) {
	bottom := min(len(g.rows), g.r+screenRows)
	n = min(n, bottom-g.r)
	copy(g.rows[g.r+n:bottom], g.rows[g.r:bottom-n])
	clear(g.rows[g.r : g.r+n])
	g.unwrapAbove()
	g.c = 0
}

// deleteRows is \e[M: the rows below, down to the bottom of the screen,
// come up over those deleted, blank ones come in at the bottom.
func (g *grid) deleteRows(n int) {
	bottom := min(len(g.rows), g.r+screenRows)
	n = min(n, bottom-g.r)
	copy(g.rows[g.r:bottom-n], g.rows[g.r+n:bottom])
	clear(g.rows[bottom-n : bottom])
	g.unwrapAbove()
	g.c = 0
}

// unwrapAbove ends the line in the row above the cursor's, whose rest has
// just been erased or moved away from under it.
func (g *grid) unwrapAbove() {
	if g.r > 0 {
		g.rows[g.r-1].wrapped = false
	}
}

// split blanks the wide character or tab that cell i is in the middle of,
// so that nothing spans the edge before i.
func (rw *row) split(i int) {
	if i <= 0 || i >= len(rw.cells) || !rw.cells[i].cont {
		return
	}
	h := i
	for h > 0 && rw.cells[h].cont {
		h--
	}
	rw.cells[h] = cell{}
	for j := h + 1; j < len(rw.cells) && rw.cells[j].cont; j++ {
		rw.cells[j] = cell{}
	}
}

// pad makes the row n cells long at least, with blanks.
func (rw *row) pad(n int) {
	for len(rw.cells) < n {
		rw.cells = append(rw.cells, cell{})
	}
}

// lines are the rows as lines of text, a wrapped row going on into the
// next one with the blanks at its end.
func (g *grid) lines() []string {
	var lines []string
	var b strings.Builder
	for i, rw := range g.rows {
		for _, c := range rw.cells {
			switch {
			case c.cont:
			case c.s == "":
				b.WriteByte(' ')
			default:
				b.WriteString(c.s)
			}
		}
		if rw.wrapped && i < len(g.rows)-1 {
			continue
		}
		lines = append(lines, strings.TrimRight(b.String(), " "))
		b.Reset()
	}
	return lines
}
