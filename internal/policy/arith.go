package policy

import (
	"errors"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// An error in arithmetic the parser takes for a syntax error, and stops
// at; to bash it is one of the command that runs it: ((1 +)) fails, the
// lines after it run, and so do the substitutions inside, $(( $(sudo ls)
// + )) runs sudo ls before it fails. So does an index a[1 +] and a ${…}
// bash cannot expand, ${x@Z}: bash parses all of them whole, by their
// brackets, and fails only when it runs them. arith rewrites such a
// construct for the parser to go on, keeping the code in it.

const (
	// maxArith is how many constructs statements rewrites in a line, on
	// top of the here-documents it closes: one at a time, as the parser
	// stops at the first.
	maxArith = 16
	// maxOpeners is how many openers before an error arith looks at for
	// the one of the construct the error is in, and maxRewrites how many
	// of those it rewrites and parses again: one inside another, and an
	// opener in a word, x[, that may be none.
	maxOpeners  = 32
	maxRewrites = 4
)

// reparsed is a line rewritten for the parser, with the statements and
// the error of the new line.
type reparsed struct {
	src   string
	stmts []*syntax.Stmt
	err   error
}

// arith rewrites the construct err is in, when bash parses it and fails
// only when it runs it: arithmetic of $((…)), ((…)), for ((…)), $[…] and
// an index a[…], or a ${…}. The text in its brackets becomes a string in
// double quotes, as bash expands arithmetic before it evaluates it: $((T))
// is $(("T")), ${T} is ${_:+"T"}, with the double quotes of T escaped, its
// backslashes escaping as in double quotes, and its substitutions kept
// whole, for the parser to see the commands in them. Single quotes in
// arithmetic quote nothing to bash, and in the string they do not either.
// ok is false when err is in no such construct, or the parser stops in the
// rewritten one again.
func arith(src string, err error) (r reparsed, ok bool) {
	var pe syntax.ParseError
	if !errors.As(err, &pe) {
		return r, false
	}
	at := int(pe.Pos.Offset())
	if at >= len(src) {
		return r, false
	}
	openers, rewrites := 0, 0
	for q := at; q >= 0 && openers < maxOpeners && rewrites < maxRewrites; q-- {
		c, found := construct(src, q)
		if !found {
			continue
		}
		openers++
		if c.close < 0 || at >= c.end {
			continue
		}
		body, quoted := dquote(src[c.open:c.close])
		if !quoted {
			continue
		}
		rewrites++
		fixed := src[:c.open] + c.prefix + body + c.suffix + src[c.close:]
		stmts, e := upToError(fixed)
		// The parser has got past the construct when it stops after it,
		// or at EOF on something open before it: what precedes it is the
		// same text.
		end := len(fixed) - (len(src) - c.end)
		var npe syntax.ParseError
		if errors.As(e, &npe) && int(npe.Pos.Offset()) >= q && int(npe.Pos.Offset()) < end {
			continue
		}
		return reparsed{fixed, stmts, e}, true
	}
	return r, false
}

// opened is a construct of bash that opens at an offset of a line: its
// text is src[open:close], what ends it src[close:end]; close is -1 when
// nothing does. prefix and suffix go around the string its text becomes.
type opened struct {
	open, close, end int
	prefix, suffix   string
}

// construct tells whether an opener of arithmetic or of ${…} is at src[q].
func construct(src string, q int) (opened, bool) {
	s := src[q:]
	switch {
	case strings.HasPrefix(s, "$(("):
		return arithEnd(src, q+3, ""), true
	case strings.HasPrefix(s, "((") && (q == 0 || src[q-1] != '$'):
		// for ((i=0; i<n; i++)) wants its two semicolons, outside the
		// string.
		suffix := ""
		if forLoop(src[:q]) {
			suffix = ";;"
		}
		return arithEnd(src, q+2, suffix), true
	case strings.HasPrefix(s, "${"):
		return ended(src, q+2, '{', '}', "_:+"), true
	case strings.HasPrefix(s, "$["):
		return ended(src, q+2, '[', ']', ""), true
	case s[0] == '[' && q > 0 && nameByte(src[q-1]):
		return ended(src, q+1, '[', ']', ""), true
	}
	return opened{}, false
}

// arithEnd is the construct of $(( or (( whose text starts at open: bash
// ends it at the ) that closes its second ( when another follows.
func arithEnd(src string, open int, suffix string) opened {
	c := opened{open: open, close: -1, suffix: suffix}
	if i := closing(src, open, '(', ')'); i >= 0 && i+1 < len(src) && src[i+1] == ')' {
		c.close, c.end = i, i+2
	}
	return c
}

// ended is the construct whose text starts at open and ends at the right
// bracket that closes it.
func ended(src string, open int, left, right byte, prefix string) opened {
	c := opened{open: open, close: -1, prefix: prefix}
	if i := closing(src, open, left, right); i >= 0 {
		c.close, c.end = i, i+1
	}
	return c
}

// forLoop tells whether before, the line before ((, ends with the keyword
// for.
func forLoop(before string) bool {
	s, ok := strings.CutSuffix(strings.TrimRight(before, " \t"), "for")
	return ok && (s == "" || strings.IndexByte(" \t\n;&|(){}", s[len(s)-1]) >= 0)
}

func nameByte(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// closing is the index in src of the right bracket that closes a left one
// before i, -1 if src ends first. Quotes, escapes and substitutions are
// skipped whole, as bash does when it looks for the end of $((…)) or ${…}.
func closing(src string, i int, left, right byte) int {
	depth := 0
	for i < len(src) {
		switch src[i] {
		case right:
			if depth == 0 {
				return i
			}
			depth--
		case left:
			depth++
		}
		if i = skip(src, i); i < 0 {
			return -1
		}
	}
	return -1
}

// skip is the index past what starts at src[i]: a quoted string, an
// escaped byte, a substitution, or else the byte alone; -1 when it does
// not end.
func skip(src string, i int) int {
	switch src[i] {
	case '\\':
		return i + 2
	case '\'':
		if j := strings.IndexByte(src[i+1:], '\''); j >= 0 {
			return i + 1 + j + 1
		}
		return -1
	case '"':
		return dquoted(src, i+1)
	case '`':
		return until(src, i+1, '`')
	case '$':
		if i+1 == len(src) {
			break
		}
		switch src[i+1] {
		case '(':
			return past(closing(src, i+2, '(', ')'))
		case '{':
			return past(closing(src, i+2, '{', '}'))
		case '[':
			return past(closing(src, i+2, '[', ']'))
		case '\'':
			return until(src, i+2, '\'')
		case '"':
			return dquoted(src, i+2)
		}
	}
	return i + 1
}

func past(i int) int {
	if i < 0 {
		return -1
	}
	return i + 1
}

// dquoted is the index past the " that ends a string started before i.
func dquoted(src string, i int) int {
	for i < len(src) {
		switch {
		case src[i] == '"':
			return i + 1
		case src[i] == '\\':
			i += 2
		case substitution(src, i):
			if i = skip(src, i); i < 0 {
				return -1
			}
		default:
			i++
		}
	}
	return -1
}

// until is the index past the end byte that ends, unescaped, a command
// substitution `…` or a string $'…' started before i.
func until(src string, i int, end byte) int {
	for i < len(src) {
		switch src[i] {
		case end:
			return i + 1
		case '\\':
			i += 2
		default:
			i++
		}
	}
	return -1
}

// substitution tells whether a substitution starts at src[i], within
// double quotes: $(…), ${…}, $[…] or `…`.
func substitution(src string, i int) bool {
	return src[i] == '`' || src[i] == '$' && i+1 < len(src) && strings.IndexByte("({[", src[i+1]) >= 0
}

// dquote is t as a string in double quotes: its double quotes escaped and
// its substitutions kept whole. ok is false when one of them does not end in
// t, or t ends in a lone backslash.
func dquote(t string) (string, bool) {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(t); {
		j := i + 1
		switch {
		case t[i] == '"':
			b.WriteByte('\\')
		case t[i] == '\\':
			if j = i + 2; j > len(t) {
				return "", false
			}
		case substitution(t, i):
			if j = skip(t, i); j < 0 {
				return "", false
			}
		}
		b.WriteString(t[i:j])
		i = j
	}
	b.WriteByte('"')
	return b.String(), true
}
