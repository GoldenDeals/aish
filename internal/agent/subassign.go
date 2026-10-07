package agent

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// setsVariable tells what in the line cmd sets a variable that may change
// what the commands of a subagent's line run, past its patterns: PATH=.
// git log runs ./git, GIT_EXTERNAL_DIFF and GIT_SSH_COMMAND have git run
// a program of their own. Commands are the line's argv as the policy
// parsed it, behind wrappers and in the code of eval and bash -c too: env,
// the builtins that set the variables their words name, and that code,
// when the patterns let them run. "" when nothing does.
//
// The script's own variables, in lower case, and the locale's pass (see
// harmlessVar); what may set a variable whose name is not written out
// does not: declare -n and -i, arithmetic on anything but numbers (bash
// evaluates the value of a variable there as arithmetic in turn, and
// x='PATH=0'; $((x)) sets PATH), a subscript, ${!x}.
func setsVariable(cmd string, commands [][]string) string {
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(cmd), "")
	if err != nil {
		return "cannot parse the command: " + err.Error()
	}
	why := ""
	syntax.Walk(f, func(n syntax.Node) bool {
		if why == "" {
			why = nodeSets(n)
		}
		return why == ""
	})
	for _, argv := range commands {
		if why != "" {
			break
		}
		why = argvSets(argv)
	}
	return why
}

func sets(name string) string {
	return fmt.Sprintf("sets %s, which may change what the commands of this line run", name)
}

// harmlessName tells whether name is a variable's that may be set: see
// harmlessVar. Anything but a name, as "$v" or a[i], may stand for any.
func harmlessName(name string) bool {
	return syntax.ValidName(name) && harmlessVar(name)
}

func nodeSets(n syntax.Node) string {
	switch n := n.(type) {
	case *syntax.Assign:
		// Without a name it is a word of declare: see DeclClause.
		if n.Name != nil && !harmlessName(n.Name.Value) {
			return sets(n.Name.Value)
		}
		return arith(n.Index)
	case *syntax.ArrayElem:
		return arith(n.Index)
	case *syntax.DeclClause:
		v := n.Variant.Value
		if v == "nameref" {
			return "nameref makes a name for another variable, which may then be set"
		}
		for _, a := range n.Args {
			switch {
			case a.Name != nil || a.Value == nil:
			case !literal(a.Value):
				return fmt.Sprintf("%s has a word made at run time, %s: what it sets is not known", v, printed(a.Value))
			default:
				if why := declArg(v, unquoted(a.Value)); why != "" {
					return why
				}
			}
		}
	case *syntax.WordIter:
		if !harmlessName(n.Name.Value) {
			return sets(n.Name.Value)
		}
	case *syntax.CoprocClause:
		if n.Name != nil && (!literal(n.Name) || !harmlessName(unquoted(n.Name))) {
			return sets(printed(n.Name))
		}
	case *syntax.ParamExp:
		return paramSets(n)
	case *syntax.ArithmExp:
		return arith(n.X)
	case *syntax.ArithmCmd:
		return arith(n.X)
	case *syntax.LetClause:
		for _, x := range n.Exprs {
			if why := arith(x); why != "" {
				return why
			}
		}
	case *syntax.CStyleLoop:
		for _, x := range []syntax.ArithmExpr{n.Init, n.Cond, n.Post} {
			if why := arith(x); why != "" {
				return why
			}
		}
	case *syntax.BinaryTest:
		switch n.Op {
		case syntax.TsEql, syntax.TsNeq, syntax.TsLeq, syntax.TsGeq, syntax.TsLss, syntax.TsGtr:
			// [[ ]] takes the operands of these as arithmetic.
			for _, x := range []syntax.TestExpr{n.X, n.Y} {
				w, ok := x.(*syntax.Word)
				if !ok {
					return "a comparison in [[ ]] that cannot be checked"
				}
				if why := arith(w); why != "" {
					return why
				}
			}
		}
	case *syntax.UnaryTest:
		if n.Op == syntax.TsVarSet || n.Op == syntax.TsRefVar {
			if w, ok := n.X.(*syntax.Word); !ok || !literal(w) || strings.Contains(unquoted(w), "[") {
				return fmt.Sprintf("[[ %s %s ]] may evaluate a subscript as arithmetic, which may set variables", n.Op, printedNode(n.X))
			}
		}
	}
	return ""
}

func printedNode(n syntax.Node) string {
	var b strings.Builder
	syntax.NewPrinter().Print(&b, n)
	return b.String()
}

// paramSets tells what in ${…} may set a variable: ${X:=v} and ${X=v},
// arithmetic in a subscript or an offset, the name ${!x} takes from x,
// whose subscript is arithmetic too.
func paramSets(p *syntax.ParamExp) string {
	if p.Exp != nil && (p.Exp.Op == syntax.AssignUnset || p.Exp.Op == syntax.AssignUnsetOrNull) && !p.Excl &&
		p.Param != nil && !harmlessName(p.Param.Value) {
		return sets(p.Param.Value)
	}
	if p.Excl && p.Names == 0 && !allIndex(p.Index) {
		x := "?"
		if p.Param != nil {
			x = p.Param.Value
		}
		return fmt.Sprintf("${!%s} takes the name of a variable from the value of %[1]s, and a subscript in it is evaluated as arithmetic, which may set variables", x)
	}
	if !allIndex(p.Index) {
		if why := arith(p.Index); why != "" {
			return why
		}
	}
	if p.Slice != nil {
		if why := arith(p.Slice.Offset); why != "" {
			return why
		}
		return arith(p.Slice.Length)
	}
	return ""
}

// allIndex tells whether a subscript is @ or *: all the elements, no
// arithmetic.
func allIndex(x syntax.ArithmExpr) bool {
	w, ok := x.(*syntax.Word)
	if !ok || len(w.Parts) != 1 {
		return false
	}
	l, ok := w.Parts[0].(*syntax.Lit)
	return ok && (l.Value == "@" || l.Value == "*")
}

// arith tells what in an arithmetic expression may set a variable: an
// assignment to one outside lower case, and anything but a number, as
// bash evaluates what a variable holds as arithmetic in turn. Of a
// variable in lower case, only = is let through, which does not read it.
func arith(x syntax.ArithmExpr) string {
	switch x := x.(type) {
	case nil:
		return ""
	case *syntax.BinaryArithm:
		switch x.Op {
		case syntax.Assgn, syntax.AddAssgn, syntax.SubAssgn, syntax.MulAssgn, syntax.QuoAssgn, syntax.RemAssgn,
			syntax.AndAssgn, syntax.OrAssgn, syntax.XorAssgn, syntax.ShlAssgn, syntax.ShrAssgn,
			syntax.AndBoolAssgn, syntax.OrBoolAssgn, syntax.XorBoolAssgn, syntax.PowAssgn:
			name, why := target(x.X)
			switch {
			case why != "":
				return why
			case !harmlessName(name):
				return sets(name)
			case x.Op != syntax.Assgn:
				return evaluated(name)
			}
			return arith(x.Y)
		}
		if why := arith(x.X); why != "" {
			return why
		}
		return arith(x.Y)
	case *syntax.UnaryArithm:
		if x.Op == syntax.Inc || x.Op == syntax.Dec {
			name, why := target(x.X)
			switch {
			case why != "":
				return why
			case !harmlessName(name):
				return sets(name)
			}
			return evaluated(name)
		}
		return arith(x.X)
	case *syntax.ParenArithm:
		return arith(x.X)
	case *syntax.Word:
		for _, p := range x.Parts {
			if p, ok := p.(*syntax.ParamExp); ok {
				if why := paramSets(p); why != "" {
					return why
				}
			}
		}
		if number(x) {
			return ""
		}
		return evaluated(printed(x))
	}
	return "arithmetic that cannot be checked"
}

// target is the name an arithmetic assignment sets: x, or a of a[i].
func target(x syntax.ArithmExpr) (string, string) {
	w, ok := x.(*syntax.Word)
	if ok && len(w.Parts) == 1 {
		switch p := w.Parts[0].(type) {
		case *syntax.Lit:
			return p.Value, ""
		case *syntax.ParamExp:
			if why := paramSets(p); why != "" {
				return "", why
			}
			if p.Param != nil && p.Index != nil && !p.Excl && p.Exp == nil && p.Slice == nil && p.Repl == nil {
				return p.Param.Value, ""
			}
		}
	}
	return "", evaluated(printedNode(x))
}

func evaluated(what string) string {
	return fmt.Sprintf("arithmetic on %s: what it holds is evaluated as arithmetic too, which may set variables; only numbers may be there", what)
}

// number tells whether w is a number of bash's arithmetic: 12, 0x1f,
// 8#17, 64#_@; a name never starts with a digit.
func number(w *syntax.Word) bool {
	if len(w.Parts) != 1 {
		return false
	}
	l, ok := w.Parts[0].(*syntax.Lit)
	if !ok || l.Value == "" || l.Value[0] < '0' || l.Value[0] > '9' {
		return false
	}
	return strings.Trim(l.Value, "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ_@#") == ""
}

// declArg tells what a word of export, declare, local, readonly or
// typeset sets that it should not: an option that makes a name for
// another variable or evaluates what is assigned, a variable outside
// lower case. The words assign even when quoted whole: export 'PATH=.'.
func declArg(variant, a string) string {
	if len(a) > 1 && (a[0] == '-' || a[0] == '+') {
		switch {
		case variant != "export" && strings.Contains(a[1:], "n"):
			// export -n takes the export away.
			return fmt.Sprintf("%s %s makes a name for another variable, which may then be set", variant, a)
		case strings.Contains(a[1:], "i"):
			return fmt.Sprintf("%s %s evaluates what is assigned as arithmetic, which may set variables", variant, a)
		}
		return ""
	}
	name, _, _ := strings.Cut(a, "=")
	if name = strings.TrimSuffix(name, "+"); !harmlessName(name) {
		return sets(name)
	}
	return ""
}

// declBuiltins are the builtins of declare, as argv when builtin or
// command runs them.
var declBuiltins = map[string]bool{"declare": true, "export": true, "local": true, "readonly": true, "typeset": true}

// nameOptions are the builtins that set the variables named by their
// words: by the options of named, and by their words past the options
// when words is set. The options of valued take a value of another kind.
var nameOptions = map[string]struct {
	named, valued string
	words         bool
}{
	"read":      {"a", "dinNptu", true},
	"mapfile":   {"", "CcdnOsu", true},
	"readarray": {"", "CcdnOsu", true},
	"unset":     {"", "", true},
	"printf":    {"v", "", false},
	"wait":      {"p", "", false},
}

// codeRunners run code their words hold, here: ssh's runs on another
// machine. The policy has the commands of that code among the line's, but
// not the variables it sets.
var codeRunners = map[string]bool{
	"alias": true, "bash": true, "dash": true, "eval": true, "flock": true, "script": true, "sh": true,
	"su": true, "trap": true, "watch": true, "zsh": true,
}

// argvSets tells which variable a command sets that it should not: env's
// NAME=value and -u NAME, the words of declare and the builtins that set
// the variables they name, what the code of eval, bash -c, su -c, trap
// and the like sets. "" when none.
func argvSets(argv []string) string {
	var names []string
	switch name := argv[0]; {
	case filepath.Base(name) == "env":
		names = envNames(argv[1:])
	case codeRunners[filepath.Base(name)]:
		var code []string
		switch filepath.Base(name) {
		case "eval":
			code = []string{strings.Join(argv[1:], " ")}
		case "alias":
			for _, a := range argv[1:] {
				_, v, _ := strings.Cut(a, "=")
				code = append(code, v)
			}
		case "bash", "dash", "sh", "zsh":
			if shellK(argv[1:]) {
				return setK
			}
			code = argv[1:]
		default:
			// The words that are no code parse as plain ones.
			code = argv[1:]
		}
		for _, c := range code {
			if why := setsVariable(c, nil); why != "" {
				return why
			}
		}
	case declBuiltins[name]:
		for _, a := range argv[1:] {
			if why := declArg(name, a); why != "" {
				return why
			}
		}
	case name == "getopts":
		if len(argv) > 2 {
			names = argv[2:3]
		}
	case name == "set":
		for _, a := range argv[1:] {
			if a == "keyword" || len(a) > 1 && a[0] == '-' && a[1] != '-' && strings.Contains(a, "k") {
				return setK
			}
		}
	case name == "shopt":
		if shoptKeyword(argv[1:]) {
			return setK
		}
	default:
		o, ok := nameOptions[name]
		if !ok {
			return ""
		}
		names = optionNames(argv[1:], o.named, o.valued, o.words)
	}
	for _, n := range names {
		if !harmlessName(n) {
			return sets(n)
		}
	}
	return ""
}

// setK is why a line that turns set -k on sets variables.
const setK = "set -k makes NAME=value among the words of any command set a variable"

// shoptKeyword tells whether shopt with the words args turns set -k on:
// -s and -o among its options, shopt -so keyword or shopt -s -o keyword,
// and keyword among the names after them.
func shoptKeyword(args []string) bool {
	opts := ""
	for i, a := range args {
		if a == "--" || len(a) < 2 || a[0] != '-' {
			if a == "--" {
				i++
			}
			return strings.Contains(opts, "s") && strings.Contains(opts, "o") && slices.Contains(args[i:], "keyword")
		}
		opts += a[1:]
	}
	return false
}

// shellK tells whether a shell with the words args starts under set -k:
// -k or -o keyword among its options, up to its first operand; a name of
// -o made at run time may be keyword.
func shellK(args []string) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--" || a == "-":
			return false
		case a == "--rcfile" || a == "--init-file":
			i++
			continue
		case strings.HasPrefix(a, "--"):
			continue
		case len(a) < 2 || a[0] != '-' && a[0] != '+':
			return false
		}
		for _, r := range a[1:] {
			switch {
			case r == 'k' && a[0] == '-':
				return true
			case (r == 'o' || r == 'O') && i+1 < len(args):
				i++
				if a[0] == '-' && r == 'o' && (args[i] == "keyword" || strings.ContainsAny(args[i], "$`")) {
					return true
				}
			}
		}
	}
	return false
}

// optionNames are the names among the words args of a builtin, as
// nameOptions has its options. Its options end at the first word that is
// not one, as bash reads them.
func optionNames(args []string, named, valued string, words bool) []string {
	var names []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" || len(a) < 2 || a[0] != '-' {
			if a == "--" {
				i++
			}
			if words && i < len(args) {
				names = append(names, args[i:]...)
			}
			break
		}
		for j := 1; j < len(a); j++ {
			c := a[j]
			if !strings.ContainsRune(named+valued, rune(c)) {
				continue
			}
			v := a[j+1:]
			if v == "" && i+1 < len(args) {
				i++
				v = args[i]
			}
			if strings.IndexByte(named, c) >= 0 {
				names = append(names, v)
			}
			break
		}
	}
	return names
}

// envNames are the variables env sets or unsets before the command it
// runs. The string of -S is split into an argv of its own among the
// commands, and looked at there.
func envNames(args []string) []string {
	var names []string
	opts := true
	for i := 0; i < len(args); i++ {
		a := args[i]
		next := func() string {
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch {
		case opts && a == "--":
			opts = false
		case opts && strings.HasPrefix(a, "--"):
			// Long options may be cut short, as long as they stay
			// unambiguous: --un=PATH.
			long, v, eq := strings.Cut(a[2:], "=")
			switch {
			case strings.HasPrefix("unset", long):
				if !eq {
					v = next()
				}
				names = append(names, v)
			case !eq && (strings.HasPrefix("chdir", long) || strings.HasPrefix("split-string", long) || strings.HasPrefix("argv0", long)):
				next()
			}
		case opts && len(a) > 1 && a[0] == '-':
			for j := 1; j < len(a); j++ {
				if c := a[j]; c == 'u' || c == 'C' || c == 'S' || c == 'a' {
					v := a[j+1:]
					if v == "" {
						v = next()
					}
					if c == 'u' {
						names = append(names, v)
					}
					break
				}
			}
		case strings.Contains(a, "="):
			name, _, _ := strings.Cut(a, "=")
			names = append(names, name)
		default:
			return names
		}
	}
	return names
}
