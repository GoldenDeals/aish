package renderer

import (
	"cmp"
	"fmt"
	"math"
	"regexp"
	"regexp/syntax"
	"slices"
	"strings"
)

// compiled is a rule with its expressions compiled.
type compiled struct {
	rule
	open, close, abort *expr
}

// expr matches with the character before it as context, so ^ and \b see what precedes the search.
type expr struct {
	re   *regexp.Regexp
	spec int // literal characters every match has
}

func compile(r rule) *compiled {
	o, c, a := r.Pattern()
	if o == "" {
		panic("renderer: a rule without Open")
	}
	return &compiled{rule: r, open: newExpr(o), close: newExpr(c), abort: newExpr(a)}
}

func newExpr(pat string) *expr {
	if pat == "" {
		return nil
	}
	re, err := syntax.Parse(pat, syntax.Perl)
	if err != nil {
		panic(fmt.Sprintf("renderer: rule %q: %v", pat, err))
	}
	return &expr{re: regexp.MustCompile(`(?m)(?s:.)(` + pat + `)`), spec: literals(re)}
}

// literals counts the literal characters every match of re has: ```mermaid is more specific than ```\w+.
func literals(re *syntax.Regexp) int {
	switch re.Op {
	case syntax.OpLiteral:
		return len(re.Rune)
	case syntax.OpCapture, syntax.OpPlus:
		return literals(re.Sub[0])
	case syntax.OpRepeat:
		return re.Min * literals(re.Sub[0])
	case syntax.OpConcat:
		n := 0
		for _, s := range re.Sub {
			n += literals(s)
		}
		return n
	case syntax.OpAlternate:
		n := math.MaxInt
		for _, s := range re.Sub {
			n = min(n, literals(s))
		}
		return n
	}
	return 0
}

// node is a zone of src; the root is all of it.
type node struct {
	r           *compiled // nil for the root
	open, close []string
	start, end  int // the zone: from Open's match to the end of Close's, its trailing line breaks left out
	from, to    int // its text
	kids        []*node
}

// match is an expr's first match from a position on.
type match struct {
	from    int // where the search started
	at, end int // at -1: none
	sub     []string
}

// key is a zone drawn: the same rule on the same text at the same width draws the same.
type key struct {
	r      *compiled
	src    string
	width  int
	closed bool
}

type parser struct {
	src        string
	rules      []*compiled
	found      map[*expr]match // each expr's last search
	opened     map[opening]*node
	last, memo map[key]string // the zones the last layout drew, and this one
}

// opening is a rule's Open matched at a position.
type opening struct {
	r  *compiled
	at int
}

// How a zone's scan ends.
const (
	closed = iota
	aborted
	unclosed // the text ended first
)

func (p *parser) parse() *node {
	p.found, p.opened = map[*expr]match{}, map[opening]*node{}
	root := &node{end: len(p.src)}
	p.scan(root)
	return root
}

// find is e's first match at pos or after it.
func (p *parser) find(e *expr, pos int) match {
	if m, ok := p.found[e]; ok && m.from <= pos && (m.at < 0 || pos <= m.at) {
		return m
	}
	m := match{from: pos, at: -1}
	ctx := "\n" + p.src // a line starts before the text; ctx[pos] is the character before src[pos]
	if loc := e.re.FindStringSubmatchIndex(ctx[pos:]); loc != nil {
		m.at, m.end = pos+loc[2]-1, pos+loc[3]-1
		for i := 2; i < len(loc); i += 2 {
			s := ""
			if loc[i] >= 0 {
				s = ctx[pos+loc[i] : pos+loc[i+1]]
			}
			m.sub = append(m.sub, s)
		}
	}
	p.found[e] = m
	return m
}

// cand is an expression matched where the scan is: n's Close or Abort, or an Open of r.
type cand struct {
	m    match
	spec int
	what int // closed, aborted, or -1 for an Open
	r    *compiled
}

// candidates are the expressions that match first after pos in n, the one to try first first: the most specific,
// then the longest, then n's Close, its Abort, and Opens in the order registered.
func (p *parser) candidates(n *node, pos int) []cand {
	var cs []cand
	add := func(e *expr, what int, r *compiled) {
		if e == nil {
			return
		}
		m := p.find(e, pos)
		if m.at < 0 || r != nil && m.end == m.at { // an empty Open would never move on
			return
		}
		if len(cs) > 0 && m.at > cs[0].m.at {
			return
		}
		if len(cs) > 0 && m.at < cs[0].m.at {
			cs = cs[:0]
		}
		cs = append(cs, cand{m, e.spec, what, r})
	}
	if n.r != nil {
		add(n.r.close, closed, nil)
		add(n.r.abort, aborted, nil)
	}
	if n.r == nil || n.r.Nests() {
		for _, r := range p.rules {
			add(r.open, -1, r)
		}
	}
	slices.SortStableFunc(cs, func(a, b cand) int {
		return cmp.Or(b.spec-a.spec, (b.m.end-b.m.at)-(a.m.end-a.m.at))
	})
	return cs
}

// scan parses n's text from n.from: the zones in it, while n nests, up to n's Close or Abort.
func (p *parser) scan(n *node) int {
	n.to = n.from
	for pos := n.from; ; {
		cs := p.candidates(n, pos)
		if len(cs) == 0 {
			n.to, n.end = len(p.src), len(p.src)
			return unclosed
		}
		var k *node
		for _, c := range cs {
			switch c.what {
			case closed:
				n.to, n.close, n.end = c.m.at, c.m.sub, trim(p.src, c.m.at, c.m.end)
				return closed
			case aborted:
				return aborted
			}
			if k = p.open(c.r, c.m); k != nil {
				break
			}
		}
		if k == nil {
			pos = cs[0].m.end // no Open makes a zone here: the text is plain
			continue
		}
		n.kids = append(n.kids, k)
		pos = k.end
	}
}

// open is the zone r opens at m, or nil if it doesn't make one: aborted, cut by the text's end with an Abort,
// or, with an Abort, closed with no text: ** is not an empty italic, while > alone is an empty quote line.
// A zone depends on nothing outside it, so each is scanned once per parse.
func (p *parser) open(r *compiled, m match) *node {
	at := opening{r, m.at}
	if k, ok := p.opened[at]; ok {
		return k
	}
	k := &node{r: r, open: m.sub, start: m.at, from: m.end}
	if r.close == nil {
		k.end = trim(p.src, m.at, m.end)
		k.from, k.to = k.end, k.end
	} else if res := p.scan(k); r.abort != nil && (res != closed || k.from == k.to) || res == aborted {
		k = nil
	}
	if k != nil && k.end == m.at { // nothing but line breaks
		k = nil
	}
	p.opened[at] = k
	return k
}

// trim is the end of src[at:end] without its trailing line breaks.
func trim(src string, at, end int) int { return at + len(strings.TrimRight(src[at:end], "\n")) }

// inner is n's text with its zones drawn at width.
func (p *parser) inner(n *node, width int) string {
	var sb strings.Builder
	at := n.from
	for _, k := range n.kids {
		sb.WriteString(p.src[at:k.start])
		sb.WriteString(p.draw(k, width))
		at = k.end
	}
	sb.WriteString(p.src[at:n.to])
	return sb.String()
}

// draw is the zone k drawn by its rule, or its source if the rule panics on it.
func (p *parser) draw(k *node, width int) (out string) {
	key := key{k.r, p.src[k.start:k.end], width, k.close != nil}
	if s, ok := p.memo[key]; ok {
		return s
	}
	if s, ok := p.last[key]; ok {
		p.memo[key] = s
		return s
	}
	defer func() {
		if recover() != nil { // a renderer's library choked on this text: show it as it came
			out = key.src
		}
		p.memo[key] = out
	}()
	z := Zone{Open: k.open, Close: k.close, Text: p.src[k.from:k.to]}
	z.inner = func(w int) string {
		if k.r.Nests() {
			return p.inner(k, w)
		}
		return z.Text
	}
	return k.r.Render(z, width)
}
