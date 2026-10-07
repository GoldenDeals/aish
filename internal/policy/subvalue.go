package policy

import (
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// A subscript in the value of an assignment holds code that runs only when
// the value is read as arithmetic, and bash expands the subscript once
// more then: y='$(id)'; x="a[$y]"; (( x )) runs id, because $y puts the
// value of y back into the subscript, which bash reads as arithmetic
// again. value sees only what is written out in the word (written drops
// $y), so the code the expansion brings is missed unless the line itself
// gives the variable its value. subval follows those values and the
// variables whose subscript the line does not resolve.

// subval holds, for a line, the values it gives its variables and which of
// them carry a subscript it cannot resolve.
type subval struct {
	// stored is the latest static value the line gave a variable, to put
	// back into a subscript that reads it (see subPut).
	stored map[string]string
	// risky names the variables the line assigns a value whose subscript
	// holds an expansion it does not resolve: read as arithmetic (( x )),
	// the expansion runs whatever it brings (see arithRead).
	risky map[string]bool
}

// indexValue looks at the value w a line gives the variable name, for the
// subscript bash reads as arithmetic later: it puts back the static values
// the line gives the variables in the subscript and checks them as code,
// and records the variable as risky when a name there is not one the line
// sets. name is "" for a value with no such variable to follow (an array
// element, the operand of [[ ]]): then only the substitution is done.
func (p *parser) indexValue(name string, w *syntax.Word) {
	ok := p.subIndex(word(w))
	if name == "" {
		return
	}
	if !ok {
		if p.subval.risky == nil {
			p.subval.risky = map[string]bool{}
		}
		p.subval.risky[name] = true
	} else {
		delete(p.subval.risky, name)
	}
	if v, isLit := literal(w); isLit {
		if p.subval.stored == nil {
			p.subval.stored = map[string]string{}
		}
		p.subval.stored[name] = v
	} else {
		delete(p.subval.stored, name)
	}
}

// subIndex looks at the subscript in text, the value a line gives a
// variable read as arithmetic later: inside the first [ a $name or ${name}
// is put back by bash and read as arithmetic again, running the code in
// the name's value (x="a[$y]"; (( x )) runs the code of y). The static
// value the line gave such a name is put back and checked as code; ok is
// false when a name there is not one the line sets, or a ${…} is more than
// a name, so what the expansion brings is not known and the value is
// computed when it is read. A $(…) and a `…` run when the value is made,
// where some command of the line shows them; they are data by then.
func (p *parser) subIndex(text string) (ok bool) {
	ok = true
	start := strings.IndexByte(text, '[')
	if start < 0 {
		return ok
	}
	depth := 0
	for i := start; i < len(text); i++ {
		switch c := text[i]; {
		case c == '\\':
			i++
		case c == '[':
			depth++
		case c == ']':
			if depth > 0 {
				depth--
			}
		case c == '`':
			if j := strings.IndexByte(text[i+1:], '`'); j >= 0 {
				i += 1 + j
			} else {
				i = len(text)
			}
		case c == '$' && i+1 < len(text) && text[i+1] == '(':
			i = skipNested(text, i+1, '(', ')')
		case c == '$' && i+1 < len(text) && text[i+1] == '{':
			close := skipNested(text, i+1, '{', '}')
			if depth > 0 && !p.subPut(braceName(text, i+2, close)) {
				ok = false
			}
			i = close
		case c == '$' && i+1 < len(text):
			name, end := plainName(text, i+1)
			i = end
			if depth > 0 && name != "" && !p.subPut(name) {
				ok = false
			}
		}
	}
	return ok
}

// subPut puts the static value the line gave the variable name back into
// the subscript and checks it as code; it is false when the line does not
// set name so, so what it holds is not known.
func (p *parser) subPut(name string) bool {
	if name == "" {
		return false
	}
	v, known := p.subval.stored[name]
	if !known {
		return false
	}
	p.dqBody(v)
	return true
}

// arithRead marks computed when arithmetic reads a variable whose value
// the line gave a subscript it does not resolve: (( x )) reads x as
// arithmetic and expands the subscript of its value once more (see
// indexValue, subval.risky).
func (p *parser) arithRead(x syntax.Node) {
	if len(p.subval.risky) == 0 {
		return
	}
	syntax.Walk(x, func(n syntax.Node) bool {
		if w, ok := n.(*syntax.Word); ok && p.subval.risky[w.Lit()] {
			p.mark(dynComputed)
		}
		return true
	})
}

// skipNested returns the index of the close that matches the open at i, or
// the last index of s when none does; s[i] is the open.
func skipNested(s string, i int, open, close byte) int {
	for depth := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case open:
			depth++
		case close:
			if depth--; depth == 0 {
				return i
			}
		}
	}
	return len(s) - 1
}

// braceName is the name of a ${…} whose { is at start-2 and } at close: the
// name alone, "" when the expansion holds more than it (${x:-y}, ${#x}).
func braceName(s string, start, close int) string {
	k := start
	for k < close && isNameByte(s[k]) {
		k++
	}
	if k == close {
		return s[start:close]
	}
	return ""
}

// plainName reads the name of a $name at start: the name, and the index of
// its last byte (start-1 for none, so the scan goes on past the $).
func plainName(s string, start int) (name string, end int) {
	k := start
	for k < len(s) && isNameByte(s[k]) {
		k++
	}
	if k == start {
		return "", start - 1
	}
	return s[start:k], k - 1
}

func isNameByte(b byte) bool {
	return b == '_' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

// dollarEscapes decodes the $'…' escapes of s that make a $ or a ` and
// leaves every other byte as written. A wrapper's NAME=VALUE is one word
// of parts the policy sees joined: where one part was $'…' and another was
// not, a $( may be split between a \x24 (or \44, $) that is $ in
// $'…' and a \x5c that stays as written in '…'. Neither the raw value nor
// the one with every escape decoded shows the $(, so it is read as the
// body of double quotes with the $ and ` escapes decoded alone, where the
// substitution it holds runs.
func dollarEscapes(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		v, n := -1, 0 // decoded value, bytes after the backslash
		switch c := s[i+1]; {
		case c == 'x':
			if x, k := digits16(s[i+2:], 2); k > 0 {
				v, n = x, 1+k
			}
		case c == 'u':
			if x, k := digits16(s[i+2:], 4); k > 0 {
				v, n = x, 1+k
			}
		case c == 'U':
			if x, k := digits16(s[i+2:], 8); k > 0 {
				v, n = x, 1+k
			}
		case c >= '0' && c <= '7':
			x, k := 0, 0
			for ; k < 3 && i+1+k < len(s) && s[i+1+k] >= '0' && s[i+1+k] <= '7'; k++ {
				x = x*8 + int(s[i+1+k]-'0')
			}
			v, n = x, k
		}
		if v == '$' || v == '`' {
			b.WriteByte(byte(v))
			i += n
			continue
		}
		b.WriteByte(s[i]) // keep the backslash; its bytes follow as written
	}
	return b.String()
}
