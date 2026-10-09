package policy

import "mvdan.cc/sh/v3/syntax"

// Code a line leaves in the user's shell for later (a trap, a function, an
// alias, bind -x, complete -C and -F) runs at the prompt or when the user
// next types a name or a key, after the request and past every check of
// the agent's calls: it is marked prompt, as an assignment to
// PROMPT_COMMAND is. It is so only in the user's shell itself: the line
// that shell runs, the code eval hands it, and the code it keeps from them
// to run later. A subshell, $(…), <(…), a pipeline but for its last
// command, a command put in the background and a shell the line starts
// (bash -c, su -c, ssh) keep what they define to themselves. The code is
// parsed all the same, and checked as the line's.

// liveNodes returns the commands and the function declarations of stmts
// that the shell running stmts runs itself: in a subshell, $(…), <(…), a
// coprocess, the background or a pipeline but for its last command they
// run in a process of their own. The last command of a pipeline runs in
// the shell under shopt -s lastpipe, and so do the commands of ${ …; }
// and ${| …; }, and the body of a function, wherever it is called.
func liveNodes(stmts []*syntax.Stmt) map[syntax.Node]bool {
	live := map[syntax.Node]bool{}
	var walk func(syntax.Node)
	walk = func(n syntax.Node) {
		syntax.Walk(n, func(n syntax.Node) bool {
			switch n := n.(type) {
			case *syntax.Subshell, *syntax.ProcSubst, *syntax.CoprocClause:
				return false
			case *syntax.CmdSubst:
				return n.TempFile || n.ReplyVar
			case *syntax.Stmt:
				return !n.Background && !n.Coprocess
			case *syntax.BinaryCmd:
				if n.Op == syntax.Pipe || n.Op == syntax.PipeAll {
					walk(n.Y)
					return false
				}
			case *syntax.CallExpr, *syntax.FuncDecl:
				live[n] = true
			}
			return true
		})
	}
	for _, s := range stmts {
		walk(s)
	}
	return live
}

// deferred marks code that the command being looked at keeps in the
// user's shell for later.
func (p *parser) deferred() {
	if p.live {
		p.mark(dynPrompt)
	}
}
