package policy

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
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
func match(pat, s string) bool {
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
