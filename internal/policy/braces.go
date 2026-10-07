package policy

import (
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// bash expands braces before it takes the quotes away, and globs with the
// quotes known: the braces, commas and brackets out of quotes work across
// the quoted text between them. {'sudo',ls} is sudo ls, s{'u',}do is sudo
// and sdo, /usr/bin/['s']udo is /usr/bin/sudo. isStatic and splits look at
// each literal of a word alone and take such a word as written; so does
// the walk of a line, and what it finds in the text as written (the code
// eval gets, a file a redirection names) stays. What bash makes of the
// word is looked at besides: placed reads the command again with such
// words made at run time (see parser.whole), and a redirection, a word of
// declare and the items of for and of an array are marked. A policy gets
// the worse of the two readings.

// unquoted is a word as bash's brace expansion and globbing see it: its
// text out of quotes as written, backslashes and all, with each quoted
// part and expansion one plain character.
func unquoted(w *syntax.Word) string {
	var b strings.Builder
	for _, p := range w.Parts {
		if lit, ok := p.(*syntax.Lit); ok {
			b.WriteString(lit.Value)
		} else {
			b.WriteByte('x')
		}
	}
	return b.String()
}

// expandsWord tells whether bash may expand the word w, as a whole, by
// braces or a glob (see expands and braceExpansion).
func expandsWord(w *syntax.Word) bool {
	s := unquoted(w)
	found, _ := braceExpansion(s)
	return found || expands(s)
}

// quotedExpansion tells whether bash may make other words of w, or another
// word, than isStatic and splits tell: braces or a bracket expression with
// quotes among them, and braces expands does not see in a literal (see
// braceExpansion).
func quotedExpansion(w *syntax.Word) bool {
	return expandsWord(w) && (isStatic(w) || !splits(w))
}

// braceExpansion tells whether bash may expand braces in s, the unquoted
// text of a word (see unquoted): a { with a } after it that closes it, as
// bash's brace_gobbler finds one, told of any { of s, also of one bash
// leaves alone (an invalid sequence): a policy errs to the side of an
// expansion. Up to the first comma bash takes a } for no end of the
// braces, so that {a}x,y} is a}x and y, and odd tells of such braces among
// those bash expands: mvdan.cc/sh, which expands them for the paths of a
// command, ends them at that }.
func braceExpansion(s string) (found, odd bool) {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '{':
			if _, ok, _, _ := braceEnd(s[i+1:]); ok {
				found = true
			}
		}
	}
	return found, found && oddBraces(s)
}

// oddBraces tells whether bash, expanding the braces of s as brace_expand
// does, goes past a } before the first comma of any: of the first { that
// has an end, then of each word between them, then of what follows.
func oddBraces(s string) bool {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '{':
			// A sequence has no } nor braces in it: when it is not valid,
			// bash looks for braces from the next character on.
			n, ok, comma, past := braceEnd(s[i+1:])
			if !ok || !comma {
				continue
			}
			if past || slices.ContainsFunc(braceWords(s[i+1:i+1+n]), oddBraces) {
				return true
			}
			return oddBraces(s[i+2+n:])
		}
	}
	return false
}

// braceEnd tells whether s, the text after a {, has the } that closes it
// for bash, at n: one out of other braces after a comma out of them, or
// after a .. before any such comma (a sequence, valid or not); past tells
// that it went past a } before.
func braceEnd(s string) (n int, ok, comma, past bool) {
	level, seq := 0, false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\\':
			i++
		case c == '{':
			level++
		case c == '}' && level > 0:
			level--
		case c == '}' && (comma || seq):
			return i, true, comma, past
		case c == '}':
			past = true
		case level > 0:
		case c == ',':
			comma = true
		case c == '.' && strings.HasPrefix(s[i+1:], ".") && !strings.HasPrefix(s[i+2:], "}"):
			// bash does not count {a..}: a .. right before a }.
			seq = true
		}
	}
	return 0, false, false, false
}

// braceWords splits the text between braces at its commas out of other
// braces, as bash does.
func braceWords(s string) []string {
	var words []string
	level, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\\':
			i++
		case c == '{':
			level++
		case c == '}' && level > 0:
			level--
		case c == ',' && level == 0:
			words = append(words, s[start:i])
			start = i + 1
		}
	}
	return append(words, s[start:])
}

// quotedRedirect marks a redirection whose file bash expands from the
// word as written: > {'/etc/x',} writes /etc/x, a file known only at run
// time.
func (p *parser) quotedRedirect(r *syntax.Redirect) {
	if quotedExpansion(r.Word) {
		p.mark(dynComputed)
		p.unknown = true
	}
}

// quotedItems marks the variable of for and select, or an array, whose
// items bash expands to others than the words as written: it gets values
// the parser does not know.
func (p *parser) quotedItems(name string, items []*syntax.Word) {
	if slices.ContainsFunc(items, quotedExpansion) {
		p.assigned(name)
	}
}

// quotedArray is quotedItems of the elements of an array assignment.
func (p *parser) quotedArray(a *syntax.Assign) {
	if a.Array == nil {
		return
	}
	var items []*syntax.Word
	for _, e := range a.Array.Elems {
		if e.Value != nil {
			items = append(items, e.Value)
		}
	}
	p.quotedItems(a.Name.Value, items)
}
