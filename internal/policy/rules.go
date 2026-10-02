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
// needs the user's yes, and a file tool writing outside the home directory
// gets WriteOutsideHome. Unlike Cedar they are not default deny: what no
// rule names is allowed, so `deny = ["sudo *"]` is a whole policy.
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
	switch in.Tool {
	case "bash":
		var texts []string
		for _, argv := range in.Commands {
			texts = append(texts, Analyze(argv, in.Cwd, in.Home).Text)
		}
		// What could not be parsed is matched as one command: a rule must
		// not stop working because of a stray quote.
		if line, _ := in.Args["command"].(string); in.ParseError != "" && line != "" {
			texts = append(texts, line)
		}
		for _, text := range texts {
			ds = append(ds, matches(Deny, c.Deny, text)...)
			ds = append(ds, matches(Ask, c.Ask, text)...)
		}
	case "write_file", "edit_file":
		if a := c.WriteOutsideHome; a != "" && a != Allow && !under(in.Path, in.Home) {
			ds = append(ds, Decision{Action: a, Reason: fmt.Sprintf("policy: writes outside home: %s", in.Path)})
		}
	}
	return combine(ds), nil
}

func matches(action string, patterns []string, text string) []Decision {
	var ds []Decision
	for _, p := range patterns {
		if match(p, text) {
			ds = append(ds, Decision{Action: action, Reason: fmt.Sprintf("policy: matches %q", p)})
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
