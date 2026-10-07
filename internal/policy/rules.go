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
			cmd := Analyze(argv, in.Cwd, in.Home)
			texts = append(texts, cmd.Text)
			// /usr/bin/sudo and ./sudo run sudo, which a pattern names.
			if len(argv) > 0 && cmd.Program != argv[0] {
				texts = append(texts, strings.Join(append([]string{cmd.Program}, cmd.Args...), " "))
			}
		}
		// What could not be parsed, or is built at run time, is matched as
		// one command too: a rule must not stop working because of a stray
		// quote or an eval.
		if in.ParseError != "" || len(in.Dynamic) > 0 {
			texts = append(texts, in.Line)
			if named := lineByName(in.Line); named != in.Line {
				texts = append(texts, named)
			}
		}
		for _, text := range texts {
			ds = append(ds, matches(Deny, c.Deny, text)...)
			ds = append(ds, matches(Ask, c.Ask, text)...)
		}
		// A command no pattern has seen may be one a pattern names: one
		// built at run time, or one past a parse error: the parser stops
		// there, and bash, which takes some lines the parser does not, may
		// go on. Without patterns nothing is forbidden, and there is
		// nothing to bypass.
		if len(c.Deny)+len(c.Ask) > 0 {
			if len(in.Dynamic) > 0 {
				ds = append(ds, Decision{Action: Ask, Reason: fmt.Sprintf("command built at run time (%s)", strings.Join(in.Dynamic, ", "))})
			}
			if in.ParseError != "" {
				ds = append(ds, Decision{Action: Ask, Reason: fmt.Sprintf("cannot parse the line (%s)", in.ParseError)})
			}
		}
		for _, path := range in.Writes {
			ds = append(ds, c.write(path, in.Home)...)
		}
		if a := c.WriteOutsideHome; a != "" && a != Allow {
			// A file known only at run time may be anywhere.
			if in.UnknownWrite {
				ds = append(ds, Decision{Action: a, Reason: "writes a file known only at run time"})
			}
			// bash runs a line up to its error, and the code the line hands
			// to a shell is in it as well, quoted: any > of a line that did
			// not parse may be a redirection.
			if in.ParseError != "" && strings.Contains(in.Line, ">") {
				ds = append(ds, Decision{Action: a, Reason: "cannot parse the line to see where it writes"})
			}
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

// lineByName is a line matched whole with its first word, taken for the
// program, called by its name: without the quotes and backslashes the
// shell takes away and without its directory.
func lineByName(line string) string {
	line = strings.TrimLeft(line, " \t\n")
	first, rest := line, ""
	if i := strings.IndexAny(line, " \t\n"); i >= 0 {
		first, rest = line[:i], line[i:]
	}
	if first = strings.NewReplacer(`\`, "", `'`, "", `"`, "").Replace(first); first != "" {
		first = filepath.Base(first)
	}
	return first + rest
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
