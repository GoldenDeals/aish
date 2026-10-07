package policy

import (
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// ${x@P} expands the value of x as bash expands a prompt string: its
// escapes decoded, then the text expanded as the body of double quotes,
// $(…) and all, with promptvars on, as it is by default: y='$(id)'; echo
// "${y@P}" runs id. No other operator of ${x@…} runs the code in a value
// (bash 5.3): @E decodes escapes only, @Q, @A, @K, @k and @a quote or
// describe the value, @U, @u and @L change its case.

// prompts follows ${x@P} through a line: the static values the line gives
// each variable, wherever it gives them, and the variables it expands so.
type prompts struct {
	values map[string][]string
	names  map[string]bool
}

// prompt looks at a ${…@P}. What it runs is computed, as what eval "$x"
// runs is: the value may come from anywhere. The code of the static values
// the line gives the variable, before or after the expansion, is kept for
// parse as well; not that of an indirect ${!x@P} or of $1.
func (p *parser) prompt(pe *syntax.ParamExp) {
	if pe.Exp == nil || pe.Exp.Op != syntax.OtherParamOps || pe.Exp.Word == nil || pe.Exp.Word.Lit() != "P" {
		return
	}
	p.mark(dynComputed)
	if pe.Excl || pe.Param == nil {
		return
	}
	name := pe.Param.Value
	if p.prompts.names[name] {
		return
	}
	if p.prompts.names == nil {
		p.prompts.names = map[string]bool{}
	}
	// Before the values: one of them may hold ${x@P} again.
	p.prompts.names[name] = true
	for _, v := range p.prompts.values[name] {
		p.promptCode(v)
	}
}

// shown keeps value, static, of the variable name for a ${name@P}, and
// the code in it when the line has one.
func (p *parser) shown(name, value string) {
	if !strings.ContainsAny(value, "$`\\") || slices.Contains(p.prompts.values[name], value) {
		return
	}
	if p.prompts.values == nil {
		p.prompts.values = map[string][]string{}
	}
	p.prompts.values[name] = append(p.prompts.values[name], value)
	if p.prompts.names[name] {
		p.promptCode(value)
	}
}

// promptCode keeps the code bash runs from value expanded as a prompt
// string: that of the body of double quotes, before and after the escapes
// are decoded; more than bash runs, never less.
func (p *parser) promptCode(value string) {
	p.dqBody(value)
	if d := promptEscapes(value); d != value {
		p.dqBody(d)
	}
}

// promptEscapes decodes the escapes of a prompt string that bash turns
// into text it expands then: \NNN, three octal digits not all 0, is a byte
// (\044 is $), \n a newline, and \\ a backslash, which escapes what
// follows it there. The others are left as they are: bash quotes what it
// makes of \w, \u or \$, and \a or \e is no quote, $ or separator.
func promptEscapes(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		switch c := s[i+1]; {
		case c == 'n':
			b.WriteByte('\n')
			i++
		case c == '\\':
			b.WriteByte('\\')
			i++
		case octal3(s[i+1:]):
			v := int(s[i+1]-'0')<<6 | int(s[i+2]-'0')<<3 | int(s[i+3]-'0')
			if v == 0 {
				// Not an escape: the backslash stays, the digits are text.
				b.WriteByte('\\')
				continue
			}
			b.WriteByte(byte(v))
			i += 3
		default:
			b.WriteByte('\\')
		}
	}
	return b.String()
}

// octal3 tells whether s starts with three octal digits.
func octal3(s string) bool {
	if len(s) < 3 {
		return false
	}
	for _, c := range []byte(s[:3]) {
		if c < '0' || c > '7' {
			return false
		}
	}
	return true
}
