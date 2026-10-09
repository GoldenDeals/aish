package proxy

import (
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// aishOnly reports whether a command line the user typed runs nothing but
// aish's own commands: `aish status`, `aish context --full > ctx.jsonl`,
// `aish session show | less`. Their output is aish's view of the session,
// the journal or the context, which the model has already; in the journal
// it would reach the model once more on every turn. `aish tool` is not
// one of them: its output is the tool's. A line that mixes them with other
// commands (`make; aish status`) has its output whole, and the model is
// owed the rest of it; a line bash cannot parse is not known to be one.
//
// The line is parsed with mvdan.cc/sh directly, not with policy.Parse:
// what is wanted is the line's own shape, which lists and pipelines it
// has, and policy.Parse flattens them into commands, adding those behind
// wrappers and in nested code, which do not decide what the line printed.
func aishOnly(line string) bool {
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(line), "")
	if err != nil || len(f.Stmts) == 0 {
		return false
	}
	for _, s := range f.Stmts {
		if !aishStmt(s) {
			return false
		}
	}
	return true
}

// aishStmt tells whether the statement runs aish's own commands only. A
// pipeline counts by its first command, the one whose output the others
// page or filter; redirections and & change nothing.
func aishStmt(s *syntax.Stmt) bool {
	switch c := s.Cmd.(type) {
	case *syntax.CallExpr:
		return aishCall(c)
	case *syntax.BinaryCmd:
		switch c.Op {
		case syntax.AndStmt, syntax.OrStmt:
			return aishStmt(c.X) && aishStmt(c.Y)
		case syntax.Pipe, syntax.PipeAll:
			return aishStmt(c.X)
		}
	}
	return false
}

// aishCall tells whether the simple command is aish with a subcommand
// other than tool: aish by its name, by a path to it, or as $AISH_BIN,
// which init.bash runs it by. A subcommand made of expansions or quotes
// may be tool.
func aishCall(c *syntax.CallExpr) bool {
	if len(c.Args) == 0 || !aishWord(c.Args[0]) {
		return false
	}
	if len(c.Args) == 1 {
		return true
	}
	sub := c.Args[1].Lit()
	return sub != "" && sub != "tool"
}

// aishWord tells whether the word names aish.
func aishWord(w *syntax.Word) bool {
	if lit := w.Lit(); lit != "" {
		return lit[strings.LastIndexByte(lit, '/')+1:] == "aish"
	}
	var b strings.Builder
	if err := syntax.NewPrinter().Print(&b, w); err != nil {
		return false
	}
	switch b.String() {
	case "$AISH_BIN", "${AISH_BIN}", `"$AISH_BIN"`, `"${AISH_BIN}"`:
		return true
	}
	return false
}
