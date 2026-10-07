package policy

import (
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// Bash evaluates some strings as it runs, and runs the code in them: the
// subscript of a name a builtin takes as text (let, unset, test -v,
// [[ -v ]], declare -n, read and the other setters) or of a variable in
// arithmetic, whose value bash reads as arithmetic in turn: x='a[$(id)]';
// (( x )) runs id. A subscript there is expanded as the body of double
// quotes, $(…) and all. So is what single quotes hold where the shell
// expands as in double quotes: in arithmetic, (( '$(id)' )), and in the
// word of "${x:-'$(id)'}". And declare takes (…) from a string for a
// compound assignment to an array: declare -a a='($(id))'. The code of such
// strings is parsed as eval's is; a value made at run time (read, the
// environment, a file, $(…)) is not seen.

// evaluates looks at a node of the line for strings bash evaluates as it
// runs, and keeps the code in them for parse.
func (p *parser) evaluates(n syntax.Node) {
	switch n := n.(type) {
	case *syntax.ArithmCmd:
		p.quoted(n.X)
	case *syntax.ArithmExp:
		p.quoted(n.X)
	case *syntax.CStyleLoop:
		p.quoted(n.Init)
		p.quoted(n.Cond)
		p.quoted(n.Post)
	case *syntax.ParamExp:
		p.quoted(n.Index)
		if n.Slice != nil {
			p.quoted(n.Slice.Offset)
			p.quoted(n.Slice.Length)
		}
		if e := n.Exp; e != nil && e.Word != nil && (e.Op == syntax.AssignUnset || e.Op == syntax.AssignUnsetOrNull) {
			// ${x:=a[\$(id)]} assigns as x=… does.
			p.value(e.Word)
		}
	case *syntax.DblQuoted:
		p.defaults(n.Parts)
	case *syntax.Redirect:
		if n.Hdoc != nil {
			// A here-document is expanded as double quotes are.
			p.defaults(n.Hdoc.Parts)
		}
	case *syntax.Assign:
		p.quoted(n.Index)
		// Without a name it is a word of declare: see declWord.
		if n.Name != nil && n.Value != nil {
			p.value(n.Value)
		}
	case *syntax.ArrayElem:
		p.quoted(n.Index)
		if n.Value != nil {
			p.value(n.Value)
		}
	case *syntax.DeclClause:
		for _, a := range n.Args {
			if a.Name == nil || a.Value == nil {
				continue
			}
			if v, ok := literal(a.Value); ok {
				p.compound(v)
			}
		}
	case *syntax.LetClause:
		p.letClause(n)
	case *syntax.UnaryTest:
		if n.Op == syntax.TsVarSet || n.Op == syntax.TsRefVar {
			p.testWord(n.X)
		}
	case *syntax.BinaryTest:
		switch n.Op {
		case syntax.TsEql, syntax.TsNeq, syntax.TsLeq, syntax.TsGeq, syntax.TsLss, syntax.TsGtr:
			// [[ ]] takes the operands of these as arithmetic.
			p.testWord(n.X)
			p.testWord(n.Y)
		}
	}
}

// value looks at the value of an assignment, which bash reads as
// arithmetic when the variable is an integer or is read in arithmetic:
// the code of its subscripts runs then. Of a value with expansions, that
// of the text written out in it: x="a[\$(id)]$y"; ((x)) runs id.
func (p *parser) value(w *syntax.Word) {
	p.subscript(written(w))
}

// testWord looks at an operand of [[ ]] that bash takes for a name or
// arithmetic. A subscript made of an expansion there is not expanded
// again.
func (p *parser) testWord(x syntax.TestExpr) {
	if w, ok := x.(*syntax.Word); ok {
		p.value(w)
	}
}

// quoted keeps the code of what single quotes hold in arithmetic, which
// bash expands as in double quotes, $'…' decoded first. The code of $(…)
// has quotes of its own.
func (p *parser) quoted(x syntax.ArithmExpr) {
	if x != nil {
		p.sglQuoted(x)
	}
}

// defaults keeps the code of what single quotes hold in the word of
// ${x:-word} and its kin in double quotes, which bash expands as the rest
// of them: "${x:-'$(id)'}" runs id. In a pattern, ${x#'…'}, they quote.
func (p *parser) defaults(parts []syntax.WordPart) {
	for _, part := range parts {
		pe, ok := part.(*syntax.ParamExp)
		if !ok || pe.Exp == nil || pe.Exp.Word == nil {
			continue
		}
		switch pe.Exp.Op {
		case syntax.AlternateUnset, syntax.AlternateUnsetOrNull, syntax.DefaultUnset, syntax.DefaultUnsetOrNull,
			syntax.ErrorUnset, syntax.ErrorUnsetOrNull, syntax.AssignUnset, syntax.AssignUnsetOrNull:
			p.sglQuoted(pe.Exp.Word)
		}
	}
}

// sglQuoted keeps the code of what the single quotes under n hold, as the
// body of double quotes, but in $(…), which has quotes of its own.
func (p *parser) sglQuoted(n syntax.Node) {
	syntax.Walk(n, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.CmdSubst, *syntax.ProcSubst:
			return false
		case *syntax.SglQuoted:
			p.dqBody(n.Value)
			if n.Dollar {
				p.dqBody(ansiC(n.Value))
			}
		}
		return true
	})
}

// subscript keeps the code of the subscripts in text that bash takes for
// a name, NAME[SUBSCRIPT], or for arithmetic, where it expands them as it
// evaluates the text: all that follows the first [ is taken for them, more
// than bash expands but never less.
func (p *parser) subscript(text string) {
	if _, sub, ok := strings.Cut(text, "["); ok && runs(sub) {
		p.dqBody(sub)
	}
}

// runs tells whether text in double quotes may run code: $(…), `…`, and
// ${…}, which may hold them or be ${ cmd; }.
func runs(s string) bool {
	return strings.Contains(s, "$(") || strings.Contains(s, "${") || strings.ContainsRune(s, '`')
}

// docStop is the line at which the parser ends a here-document it parses
// alone: text after it would not be parsed.
const docStop = "MVDAN_CC_SH_SYNTAX_EOF"

// dqBody keeps the code of text that bash expands as the body of double
// quotes: what $(…) and `…` run, wherever they are in it, and ${x@P} (see
// prompt). Quotes are text there, and what single ones hold is expanded
// too. Text that does not parse is marked.
func (p *parser) dqBody(text string) {
	if !strings.ContainsAny(text, "$`") {
		return
	}
	if strings.Contains(text, docStop) {
		p.mark(dynComputed)
		return
	}
	w, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Document(strings.NewReader(text))
	if err != nil {
		p.mark(dynComputed)
		return
	}
	syntax.Walk(w, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.CmdSubst:
			var b strings.Builder
			if syntax.NewPrinter().Print(&b, &syntax.File{Stmts: n.Stmts}) != nil {
				p.mark(dynComputed)
			}
			p.evals = append(p.evals, b.String())
			return false
		case *syntax.SglQuoted:
			p.dqBody(n.Value)
			if n.Dollar {
				p.dqBody(ansiC(n.Value))
			}
			return false
		case *syntax.ParamExp:
			p.prompt(n)
		}
		return true
	})
}

// compound keeps the code of a value that declare and its kin assign from
// a string: (…) is a compound assignment to an array, declared one or not,
// whose words are expanded as if written out, $(…) and <(…) among them.
func (p *parser) compound(v string) {
	if !strings.HasPrefix(v, "(") || !strings.HasSuffix(v, ")") {
		return
	}
	if !strings.ContainsAny(v, "$`") && !strings.Contains(v, "<(") && !strings.Contains(v, ">(") {
		return
	}
	src := "_=" + v
	if _, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(src), ""); err != nil {
		p.mark(dynComputed)
		return
	}
	p.evals = append(p.evals, src)
}

// declValue looks at a value that declare and its kin assign from a word
// quoted whole or run as a command: a compound assignment, or a value read
// as arithmetic later.
func (p *parser) declValue(v string) {
	p.compound(v)
	p.subscript(v)
}

// letClause looks at let as bash runs it: a builtin whose words, expanded
// as any command's, are arithmetic. The parser reads them as arithmetic of
// its own; printed back, they are read as words. A glob in one leaves it
// as written or makes a file's name of it; braces make other words.
func (p *parser) letClause(l *syntax.LetClause) {
	var b strings.Builder
	b.WriteString("builtin ")
	if syntax.NewPrinter().Print(&b, l) != nil {
		p.mark(dynComputed)
		return
	}
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(b.String()), "")
	if err != nil || len(f.Stmts) != 1 {
		p.mark(dynComputed)
		return
	}
	call, ok := f.Stmts[0].Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) < 2 {
		p.mark(dynComputed)
		return
	}
	words := call.Args[2:]
	args, static := make([]string, len(words)), make([]bool, len(words))
	for i, w := range words {
		args[i], static[i] = literal(w)
		if !static[i] || braced(w) {
			args[i], static[i] = word(w), false
		}
	}
	p.let(args, static)
}

// braced tells whether brace expansion may make other words of w: a brace
// out of quotes, which may pair with one in another part of it.
func braced(w *syntax.Word) bool {
	return slices.ContainsFunc(w.Parts, func(part syntax.WordPart) bool {
		lit, ok := part.(*syntax.Lit)
		return ok && strings.ContainsAny(lit.Value, "{}")
	})
}

// let evaluates its words as arithmetic.
func (p *parser) let(args []string, static []bool) []string {
	for i, a := range args {
		p.evaluated(a, static[i])
	}
	return nil
}

// test -v NAME and [ -v NAME ] expand the subscript of NAME. A word made
// at run time may be -v.
func (p *parser) test(args []string, static []bool) []string {
	for i := 1; i < len(args); i++ {
		if !static[i-1] || args[i-1] == "-v" {
			p.evaluated(args[i], static[i])
		}
	}
	return nil
}

// evaluated looks at a word, in its source form, that a builtin evaluates
// as a name or arithmetic. In one made at run time a subscript with an
// expansion of the line is expanded again: what the expansion gives runs
// as code, so it is marked. What is written out in it, as \$( or the
// \x24( of $'…', is parsed all the same.
func (p *parser) evaluated(a string, static bool) {
	if static {
		p.subscript(a)
		return
	}
	p.subscript(ansiC(a))
	if expanded(a) {
		p.mark(dynComputed)
	}
}

// expanded tells whether a word made at run time, in its source form, may
// have a subscript with an expansion in it: a[$i], but not ${a[$i]}, which
// the shell has expanded by then.
func expanded(s string) bool {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\\':
			i++
		case c == '$' && i+1 < len(s) && (s[i+1] == '{' || s[i+1] == '('):
			if depth > 0 {
				return true
			}
			right := byte('}')
			if s[i+1] == '(' {
				right = ')'
			}
			if i = closing(s, i+2, s[i+1], right); i < 0 {
				return false
			}
		case c == '$' || c == '`':
			if depth > 0 {
				return true
			}
		case c == '[':
			depth++
		case c == ']' && depth > 0:
			depth--
		}
	}
	return false
}

// literal is the text the shell makes of w when nothing in it expands at
// run time, as it makes the value of an assignment: a glob or braces stay
// as written there, and $'…' is decoded.
func literal(w *syntax.Word) (string, bool) {
	var b strings.Builder
	for _, part := range w.Parts {
		switch part := part.(type) {
		case *syntax.Lit:
			b.WriteString(unescape(part.Value))
		case *syntax.SglQuoted:
			if part.Dollar {
				b.WriteString(ansiC(part.Value))
			} else {
				b.WriteString(part.Value)
			}
		case *syntax.DblQuoted:
			for _, q := range part.Parts {
				lit, ok := q.(*syntax.Lit)
				if !ok {
					return "", false
				}
				b.WriteString(unescapeOnly(lit.Value, "$`\"\\"))
			}
		default:
			return "", false
		}
	}
	return b.String(), true
}

// written is the text written out in w, as literal makes it, with what its
// expansions give left out: that may be nothing.
func written(w *syntax.Word) string {
	var b strings.Builder
	for _, part := range w.Parts {
		switch part := part.(type) {
		case *syntax.DblQuoted:
			for _, q := range part.Parts {
				if lit, ok := q.(*syntax.Lit); ok {
					b.WriteString(unescapeOnly(lit.Value, "$`\"\\"))
				}
			}
		case *syntax.Lit, *syntax.SglQuoted:
			s, _ := literal(&syntax.Word{Parts: []syntax.WordPart{part}})
			b.WriteString(s)
		}
	}
	return b.String()
}

// ansiC decodes the escapes of $'…' as bash does: \x24 is $.
func ansiC(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch c := s[i]; c {
		case 'a':
			b.WriteByte('\a')
		case 'b':
			b.WriteByte('\b')
		case 'e', 'E':
			b.WriteByte(0x1b)
		case 'f':
			b.WriteByte('\f')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'v':
			b.WriteByte('\v')
		case '\\', '\'', '"', '?':
			b.WriteByte(c)
		case 'c':
			if i+1 < len(s) {
				i++
				b.WriteByte(s[i] & 0x1f)
			} else {
				b.WriteString(`\c`)
			}
		case 'x', 'u', 'U':
			n := 2
			switch c {
			case 'u':
				n = 4
			case 'U':
				n = 8
			}
			v, k := digits16(s[i+1:], n)
			switch {
			case k == 0:
				b.WriteByte('\\')
				b.WriteByte(c)
			case c == 'x':
				b.WriteByte(byte(v))
			default:
				b.WriteRune(rune(v))
			}
			i += k
		case '0', '1', '2', '3', '4', '5', '6', '7':
			v, k := 0, 0
			for ; k < 3 && i+k < len(s) && s[i+k] >= '0' && s[i+k] <= '7'; k++ {
				v = v*8 + int(s[i+k]-'0')
			}
			b.WriteByte(byte(v))
			i += k - 1
		default:
			b.WriteByte('\\')
			b.WriteByte(c)
		}
	}
	return b.String()
}

// digits16 reads up to n hex digits at the start of s: their value and
// how many there are.
func digits16(s string, n int) (v, k int) {
	for ; k < n && k < len(s); k++ {
		d := strings.IndexByte("0123456789abcdefABCDEF", s[k])
		if d < 0 {
			break
		}
		if d > 15 {
			d -= 6
		}
		v = v*16 + d
	}
	return v, k
}

// takeEvals returns the code kept from the strings of the line walked so
// far, once each, and forgets it.
func (p *parser) takeEvals() []string {
	var out []string
	for _, s := range p.evals {
		if strings.TrimSpace(s) != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	p.evals = nil
	return out
}
