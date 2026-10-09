package capture

import (
	"math/rand/v2"
	"slices"
)

// shiftMax is how many runes an edit of Clean's line moves or blanks in
// place. One that would take more cuts the line into pieces first: \e[@ or
// \e[P at the start of a long line, again and again, each went along all
// of it, and a stream of a hundred kilobytes took seconds.
const shiftMax = 256

// textLine is the line Clean draws on, of any length: a tail edited in
// place, the way a terminal line is, and before it the pieces the edits
// too long for that cut the line into.
type textLine struct {
	head *piece
	tail []rune
}

func (l *textLine) len() int { return l.head.size() + len(l.tail) }

// put writes r at col, the line padded with blanks up to it.
func (l *textLine) put(col int, r rune) {
	h := l.head.size()
	if col < h {
		l.head = l.head.put(col, r)
		return
	}
	col -= h
	for len(l.tail) < col {
		l.tail = append(l.tail, ' ')
	}
	if col < len(l.tail) {
		l.tail[col] = r
	} else {
		l.tail = append(l.tail, r)
	}
}

// insert pushes the runes from col on n blanks right; past the end of the
// line there is nothing to push.
func (l *textLine) insert(col, n int) {
	if col >= l.len() {
		return
	}
	if c := col - l.head.size(); c >= 0 && len(l.tail)-c <= shiftMax {
		l.tail = slices.Insert(l.tail, c, slices.Repeat([]rune{' '}, n)...)
		return
	}
	l.cut()
	l.head = replace(l.head, col, col, spaces(n))
}

// remove deletes n runes at col, the rest of the line coming left.
func (l *textLine) remove(col, n int) {
	end := min(col+n, l.len())
	if col >= end {
		return
	}
	h := l.head.size()
	if c := col - h; c >= 0 && len(l.tail)-c <= shiftMax {
		l.tail = slices.Delete(l.tail, c, end-h)
		return
	}
	l.cut()
	l.head = replace(l.head, col, end, nil)
}

// blank turns the runes from from up to to into blanks; the line stays as
// long as it is.
func (l *textLine) blank(from, to int) {
	to = min(to, l.len())
	if from >= to {
		return
	}
	h := l.head.size()
	if from >= h && to-from <= shiftMax {
		for i := from - h; i < to-h; i++ {
			l.tail[i] = ' '
		}
		return
	}
	l.cut()
	l.head = replace(l.head, from, to, spaces(to-from))
}

// truncate drops the runes from n on.
func (l *textLine) truncate(n int) {
	if h := l.head.size(); n < h {
		l.head, _ = split(l.head, n)
		l.tail = l.tail[:0]
	} else {
		l.tail = l.tail[:min(n-h, len(l.tail))]
	}
}

func (l *textLine) String() string {
	if l.head == nil {
		return string(l.tail)
	}
	return string(append(l.head.appendTo(make([]rune, 0, l.len())), l.tail...))
}

func (l *textLine) reset() {
	l.head, l.tail = nil, l.tail[:0]
}

// cut makes the tail the last piece. The new tail does not share its
// runes: the pieces are written in place.
func (l *textLine) cut() {
	if n := len(l.tail); n > 0 {
		l.head = merge(l.head, newPiece(l.tail[:n:n], 0))
		l.tail = nil
	}
}

// piece is a stretch of a line, runes or blanks, and the root of a treap
// of the stretches around it in the order of the line: cutting the line
// anywhere, putting a piece in or taking one out is a few steps down it
// however long the line is.
type piece struct {
	left, right *piece
	prio        uint32
	runes       int    // in the treap it is the root of
	text        []rune // nil for blanks
	blanks      int
}

func newPiece(text []rune, blanks int) *piece {
	return (&piece{prio: rand.Uint32(), text: text, blanks: blanks}).fix()
}

func spaces(n int) *piece { return newPiece(nil, n) }

func (t *piece) size() int {
	if t == nil {
		return 0
	}
	return t.runes
}

// own is how long the stretch of t itself is.
func (t *piece) own() int { return len(t.text) + t.blanks }

func (t *piece) fix() *piece {
	t.runes = t.left.size() + t.own() + t.right.size()
	return t
}

// split cuts the line t is the root of after n runes.
func split(t *piece, n int) (*piece, *piece) {
	switch {
	case n <= 0:
		return nil, t
	case n >= t.size():
		return t, nil
	}
	ls := t.left.size()
	switch {
	case n <= ls:
		l, r := split(t.left, n)
		t.left = r
		return l, t.fix()
	case n >= ls+t.own():
		l, r := split(t.right, n-ls-t.own())
		t.right = l
		return t.fix(), r
	}
	n -= ls
	rest := &piece{prio: rand.Uint32()}
	if t.text != nil {
		rest.text, t.text = t.text[n:], t.text[:n:n]
	} else {
		rest.blanks, t.blanks = t.blanks-n, n
	}
	r := merge(rest.fix(), t.right)
	t.right = nil
	return t.fix(), r
}

// merge joins the lines a and b are the roots of, a first.
func merge(a, b *piece) *piece {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case a.prio > b.prio:
		a.right = merge(a.right, b)
		return a.fix()
	}
	b.left = merge(a, b.left)
	return b.fix()
}

// replace puts p in place of the runes of t from i up to j.
func replace(t *piece, i, j int, p *piece) *piece {
	l, r := split(t, i)
	_, r = split(r, j-i)
	return merge(merge(l, p), r)
}

// put writes r at i, within the line t is the root of. Into a long stretch
// of blanks it writes a short one cut out of it: blanks put in by \e[@ or
// \e[X are not spelled out until written in.
func (t *piece) put(i int, r rune) *piece {
	p, at := t, i
	for {
		ls := p.left.size()
		if at < ls {
			p = p.left
			continue
		}
		at -= ls
		if at < p.own() {
			break
		}
		at -= p.own()
		p = p.right
	}
	if p.text == nil {
		const stretch = 64
		if p.blanks > stretch {
			text := slices.Repeat([]rune{' '}, min(stretch, p.blanks-at))
			text[0] = r
			return replace(t, i, i+len(text), newPiece(text, 0))
		}
		p.text, p.blanks = slices.Repeat([]rune{' '}, p.blanks), 0
	}
	p.text[at] = r
	return t
}

func (t *piece) appendTo(b []rune) []rune {
	if t == nil {
		return b
	}
	b = t.left.appendTo(b)
	b = append(b, t.text...)
	for range t.blanks {
		b = append(b, ' ')
	}
	return t.right.appendTo(b)
}
