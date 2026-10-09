package proxy

// touches reports whether o writes into the status's cells, or may, or
// takes the status off its line.
func (l *inputLine) touches(o op) bool {
	switch o.kind {
	case opLost:
		return true
	case 's':
		return true // the shell's \e7: ours would overwrite what it saves
	case opLF:
		return !l.keyed // the status goes down to the new line
	case opPrint:
		row, col := l.put(o.n)
		return o.n > 0 && row == 0 && col+o.n > l.s
	case 'K':
		return l.row == 0 && (o.n != 1 || l.col >= l.s)
	case 'J':
		return l.row <= 0
	case 'P', '@':
		return l.row == 0
	case 'X':
		return l.row == 0 && l.col+o.n > l.s
	}
	return false
}

// put is where a character w cells wide goes: on the next line if this one
// has no room left for it.
func (l *inputLine) put(w int) (row, col int) {
	if l.wrap || l.col+w > l.cols {
		return l.row + 1, 0
	}
	return l.row, l.col
}

func (l *inputLine) apply(o op) {
	switch o.kind {
	case opNone:
		return
	case opLost:
		l.lost = true
		return
	case opString:
		l.str = true
		return
	case opPrint:
		if o.n == 0 {
			return // combining: goes with the character before
		}
		l.row, l.col = l.put(o.n)
		l.col += o.n
		l.ends[l.row] = max(l.ends[l.row], l.col)
		l.wrap = l.col == l.cols
		if l.wrap {
			l.col--
		}
		return
	case opLF:
		l.row++
		if !l.keyed {
			l.follow()
		}
	case opTab:
		l.col = min((l.col/8+1)*8, l.cols-1)
	case 'A':
		l.row -= o.n
	case 'B':
		l.row += o.n
	case 'C':
		l.col = min(l.col+o.n, l.cols-1)
	case 'D':
		l.col = max(l.col-o.n, 0)
	case 'G':
		l.col = min(o.n, l.cols) - 1
	case 'K':
		switch o.n {
		case 0:
			l.ends[l.row] = min(l.ends[l.row], l.col)
		case 1:
			if l.ends[l.row] <= l.col+1 {
				l.ends[l.row] = 0
			}
		case 2:
			l.ends[l.row] = 0
		}
	case 'J':
		l.ends[l.row] = min(l.ends[l.row], l.col)
		for r := range l.ends {
			if r > l.row {
				delete(l.ends, r)
			}
		}
	case 'P':
		if e := l.ends[l.row]; e > l.col {
			l.ends[l.row] = max(l.col, e-o.n)
		}
	case '@':
		if e := l.ends[l.row]; e > l.col {
			l.ends[l.row] = min(l.cols, e+o.n)
		}
	case 'X':
		if e := l.ends[l.row]; l.col < e && l.col+o.n >= e {
			l.ends[l.row] = l.col
		}
	case 's':
		l.saved, l.held = [2]int{l.row, l.col}, true
	case 'u':
		l.row, l.col = l.saved[0], l.saved[1]
		l.held = false
	}
	l.wrap = false
}

// follow makes the cursor's line the status's, as a new line before the
// first key is the prompt's: rows count from it now.
func (l *inputLine) follow() {
	d := l.row
	ends := make(map[int]int, len(l.ends))
	for r, e := range l.ends {
		ends[r-d] = e
	}
	l.ends = ends
	l.saved[0] -= d
	l.row = 0
	l.down = true
}
