package policy

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"mvdan.cc/sh/v3/syntax"
)

// Rules are the simple policies of the [policy] table in config.toml: a
// command matching a Deny pattern is denied, one matching an Ask pattern
// needs the user's yes, and a file tool or a redirection writing outside
// the home directory gets WriteOutsideHome. Unlike Cedar they are not
// default deny: what no rule names is allowed, so `deny = ["sudo *"]` is a
// whole policy.
type Rules struct {
	Deny []string
	Ask  []string
	// WriteOutsideHome is allow, ask or deny; "" is allow.
	WriteOutsideHome string
}

// Len is the number of rules, for `aish policy`.
func (r Rules) Len() int {
	n := len(r.Deny) + len(r.Ask)
	if r.WriteOutsideHome != "" && r.WriteOutsideHome != Allow {
		n++
	}
	return n
}

func (r Rules) check() error {
	switch r.WriteOutsideHome {
	case "", Allow, Ask, Deny:
		return nil
	}
	return fmt.Errorf("policy: write_outside_home = %q: want %q, %q or %q", r.WriteOutsideHome, Allow, Ask, Deny)
}

type rulesChecker struct{ Rules }

func (c rulesChecker) Check(ctx context.Context, in Input) (Decision, error) {
	var ds []Decision
	switch {
	case in.Line != "":
		var texts []string
		for _, argv := range in.Commands {
			texts = append(texts, Analyze(argv, in.Cwd, in.Home).Text)
		}
		// What could not be parsed, or is built at run time, is matched as
		// one command too: a rule must not stop working because of a stray
		// quote or an eval.
		if in.ParseError != "" || len(in.Dynamic) > 0 {
			texts = append(texts, in.Line)
		}
		for _, text := range texts {
			ds = append(ds, matches(Deny, c.Deny, text)...)
			ds = append(ds, matches(Ask, c.Ask, text)...)
		}
		// A command no pattern has seen may be one a pattern names. Without
		// patterns nothing is forbidden, and there is nothing to bypass.
		if len(in.Dynamic) > 0 && len(c.Deny)+len(c.Ask) > 0 {
			ds = append(ds, Decision{Action: Ask, Reason: fmt.Sprintf("command built at run time (%s)", strings.Join(in.Dynamic, ", "))})
		}
		for _, path := range in.Writes {
			ds = append(ds, c.write(path, in.Home)...)
		}
		// A file known only at run time may be anywhere. Parse marks it
		// computed, so a line without the mark writes none.
		if a := c.WriteOutsideHome; a != "" && a != Allow && slices.Contains(in.Dynamic, dynComputed) && unknownWrite(in.Line, in.shell().pwd, in.shell().home) {
			ds = append(ds, Decision{Action: a, Reason: "writes a file known only at run time"})
		}
	case in.Tool == "write_file", in.Tool == "edit_file":
		ds = append(ds, c.write(in.Path, in.Home)...)
	}
	return combine(ds), nil
}

// write is WriteOutsideHome for a write to path.
func (c rulesChecker) write(path, home string) []Decision {
	if a := c.WriteOutsideHome; a != "" && a != Allow && !under(path, home) {
		return []Decision{{Action: a, Reason: fmt.Sprintf("writes outside home: %s", path)}}
	}
	return nil
}

// unknownWrite tells whether a redirection in line writes a file known only
// at run time: one built of expansions (> "$f") or a relative one in a line
// with a cd. Parse marks both computed, as it marks a program built at run
// time, yet `$cmd > ~/x` writes a known file; so the line is walked again
// as Parse walks it, the code it hands to a shell included, and only its
// redirections are asked about.
func unknownWrite(line, cwd, home string) bool {
	p := &parser{kinds: map[string]bool{}, cwd: cwd, home: home}
	unknown := false
	todo := []snippet{{src: line}}
	for depth := 0; depth <= maxDepth && len(todo) > 0 && !unknown; depth++ {
		var nested []snippet
		for _, s := range todo {
			f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(s.src), "")
			if err != nil {
				continue
			}
			p.remote = s.remote
			done := map[*syntax.CallExpr]bool{}
			syntax.Walk(f, func(n syntax.Node) bool {
				switch n := n.(type) {
				case *syntax.Stmt:
					if call, ok := n.Cmd.(*syntax.CallExpr); ok {
						done[call] = true
						nested = append(nested, p.call(call, n.Redirs)...)
					}
				case *syntax.CallExpr:
					if !done[n] {
						nested = append(nested, p.call(n, nil)...)
					}
				case *syntax.Redirect:
					// A parser of its own: the marks of p are the
					// programs' too.
					r := &parser{kinds: map[string]bool{}, cwd: cwd, home: home, remote: s.remote}
					r.redirect(n)
					unknown = unknown || r.kinds[dynComputed]
					p.writes = append(p.writes, r.writes...)
				}
				return true
			})
		}
		todo = nested
	}
	for _, w := range p.writes {
		if w.rel && p.chdir {
			return true
		}
	}
	return unknown
}

func matches(action string, patterns []string, text string) []Decision {
	var ds []Decision
	for _, p := range patterns {
		if match(p, text) {
			ds = append(ds, Decision{Action: action, Reason: fmt.Sprintf("matches %q", p)})
		}
	}
	return ds
}

// under tells whether path is dir or inside it. Both are absolute and
// resolved; no home at all is nobody's home, so nothing is under it.
func under(path, dir string) bool {
	if path == "" || dir == "" {
		return false
	}
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

// match is a glob where * is any run of characters, spaces and slashes
// included, and ? is one character; there are no classes or escapes. It
// is not filepath.Match, whose * stops at a slash: `sudo *` must take
// `sudo cat /etc/x`. Only the last * is ever backtracked to, so a pattern
// with several of them stays linear in the length of a long command.
//
// A pattern ending in " *" takes the bare command too: who writes
// `sudo *` means any sudo, and sudo with no arguments is a root shell, as
// is the value of `alias s=sudo`, which is a command of its own.
func match(pat, s string) bool {
	if head, ok := strings.CutSuffix(pat, " *"); ok && glob(head, s) {
		return true
	}
	return glob(pat, s)
}

func glob(pat, s string) bool {
	p, i := 0, 0
	star, from := -1, 0
	for i < len(s) {
		if p < len(pat) {
			switch pat[p] {
			case '*':
				star, from = p, i
				p++
				continue
			case '?':
				_, n := utf8.DecodeRuneInString(s[i:])
				p, i = p+1, i+n
				continue
			default:
				if pat[p] == s[i] {
					p, i = p+1, i+1
					continue
				}
			}
		}
		if star < 0 {
			return false
		}
		_, n := utf8.DecodeRuneInString(s[from:])
		from += n
		p, i = star+1, from
	}
	for p < len(pat) && pat[p] == '*' {
		p++
	}
	return p == len(pat)
}
