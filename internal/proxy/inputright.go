package proxy

import "fmt"

// The text a shell prints apart at the right of the prompt's line — zsh's
// RPROMPT, a right-aligned part of a bash prompt — takes the cells the
// status is drawn in. Drawn there, either would cover the other; so the
// status goes left of that text, with a column between them, and stays off
// only when the line has no room left for it.
//
// right is the first column of that text on the status's line, 0 when
// there is none; left is where the text before it ends. The text is a run
// printed past the end of the line, with a gap before it, into the cells
// the status takes at the right edge. What changes that line in a way the
// model cannot follow for sure (deleting or inserting characters, erasing
// some of them, a new line before the first key) ends the run: the status,
// with no room at the edge, stays off.

// place is the column the status starts at: the right edge, left of the
// text at the right, or -1 for none.
func (l *inputLine) place() int {
	if l.ends[0] < l.s {
		return l.s
	}
	if at := l.right - 1 - l.w; l.right > 0 && at > l.left {
		return at
	}
	return -1
}

// lineEnd is where what is printed on row r ends, the text at the right
// aside.
func (l *inputLine) lineEnd(r int) int {
	if r == 0 && l.right > 0 {
		return l.left
	}
	return l.ends[r]
}

// noteRight follows the text at the right through o, before the model
// applies it.
func (l *inputLine) noteRight(o op) {
	switch o.kind {
	case opPrint:
		if o.n == 0 {
			return
		}
		row, col := l.put(o.n)
		switch {
		case row != 0:
		case l.right > 0 && col >= l.right:
		case l.right > 0:
			l.left = max(l.left, col+o.n)
			if l.left >= l.right {
				l.right = 0 // one text now
			}
		case col > l.ends[0] && col+o.n > l.s:
			l.right, l.left = col, l.ends[0]
		}
	case 'K':
		if l.row == 0 && (o.n != 0 || l.col <= l.right) {
			l.right = 0
		}
	case 'J':
		if l.row < 0 || l.row == 0 && l.col <= l.right {
			l.right = 0
		}
	case 'P', '@', 'X':
		if l.row == 0 {
			l.right = 0
		}
	case opLF:
		if !l.keyed {
			l.right = 0 // the next line is the status's
		}
	}
}

// hits is touches for the status where it is drawn: left of the text at
// the right it ends before the edge.
func (l *inputLine) hits(o op) bool {
	if l.at == l.s {
		return l.touches(o)
	}
	end := l.at + l.w
	switch o.kind {
	case opPrint:
		row, col := l.put(o.n)
		return o.n > 0 && row == 0 && col < end && col+o.n > l.at
	case 'K':
		return l.row == 0 && (o.n == 2 || o.n == 0 && l.col < end || o.n == 1 && l.col >= l.at)
	case 'J':
		return l.row < 0 || l.row == 0 && l.col < end
	case 'P', '@':
		return l.row == 0 && l.col < end
	case 'X':
		return l.row == 0 && l.col < end && l.col+o.n > l.at
	}
	return l.touches(o)
}

// clear erases the status's cells: to the end of the line at the edge, the
// cells alone left of the text at the right.
func (l *inputLine) clear(out []byte) []byte {
	if l.at == l.s {
		return append(out, "\x1b[K"...)
	}
	return fmt.Appendf(out, "\x1b[%dX", l.w)
}
