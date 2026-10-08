package policy

import (
	"slices"

	"mvdan.cc/sh/v3/syntax"
)

// GlobalAliases tells the input the names of the global aliases (alias
// -g) of the zsh the call is made in, as far as they are known: zsh puts
// the value of one in place of a word of the line that is its name, and
// even `echo x G` may then pipe to sh. The policy does not read that
// value: a line with such a word is computed, and so is the code of its
// eval. A bash has none, nor a zsh the line starts. Call it before
// HandOff.
func (in *Input) GlobalAliases(names []string) {
	if in.sh.zsh != nil {
		in.sh.zsh.aliases = names
	}
}

// aliased tells whether a word of f, code z reads, is the name of one of
// its global aliases: a word by itself, neither quoted nor escaped, where
// zsh reads one — a command, an argument, the target of a redirection, an
// item of for or of an array, a pattern of case, an operand of [[ ]], the
// name of a function. A word inside another, as in x=G or ${x:-G}, is not.
func (z *zshShell) aliased(f *syntax.File) bool {
	if len(z.aliases) == 0 {
		return false
	}
	found := false
	name := func(s string) {
		if slices.Contains(z.aliases, s) {
			found = true
		}
	}
	word := func(w *syntax.Word) {
		if w == nil || len(w.Parts) != 1 {
			return
		}
		if lit, ok := w.Parts[0].(*syntax.Lit); ok {
			name(lit.Value)
		}
	}
	test := func(x syntax.TestExpr) {
		if w, ok := x.(*syntax.Word); ok {
			word(w)
		}
	}
	syntax.Walk(f, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.CallExpr:
			for _, w := range n.Args {
				word(w)
			}
		case *syntax.Redirect:
			word(n.Word)
		case *syntax.WordIter:
			for _, w := range n.Items {
				word(w)
			}
		case *syntax.ArrayExpr:
			for _, e := range n.Elems {
				word(e.Value)
			}
		case *syntax.CaseClause:
			word(n.Word)
			for _, item := range n.Items {
				for _, w := range item.Patterns {
					word(w)
				}
			}
		case *syntax.DeclClause:
			for _, a := range n.Args {
				if a.Naked && a.Name != nil {
					name(a.Name.Value)
				} else if a.Naked {
					word(a.Value)
				}
			}
		case *syntax.TestClause:
			test(n.X)
		case *syntax.UnaryTest:
			test(n.X)
		case *syntax.BinaryTest:
			test(n.X)
			test(n.Y)
		case *syntax.FuncDecl:
			name(n.Name.Value)
		}
		return !found
	})
	return found
}
