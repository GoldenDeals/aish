package policy

import (
	"path/filepath"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

const maxDepth = 4

// Commands parses a bash command line and returns the argv of every simple
// command in it. Words that are not static (expansions, substitutions) are
// kept in their source form, e.g. "$HOME".
func Commands(src string) ([][]string, error) {
	var out [][]string
	err := commands(src, 0, &out)
	return out, err
}

func commands(src string, depth int, out *[][]string) error {
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(src), "")
	if err != nil {
		return err
	}
	var nested []string
	syntax.Walk(f, func(n syntax.Node) bool {
		call, ok := n.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		argv := make([]string, len(call.Args))
		for i, w := range call.Args {
			argv[i] = word(w)
		}
		*out = append(*out, argv)
		for inner := unwrap(argv); inner != nil; inner = unwrap(inner) {
			*out = append(*out, inner)
		}
		if s, ok := shellC(argv); ok {
			nested = append(nested, s)
		}
		return true
	})
	if depth < maxDepth {
		for _, s := range nested {
			if err := commands(s, depth+1, out); err != nil {
				return err
			}
		}
	}
	return nil
}

// wrappers run their arguments as a command.
var wrappers = map[string]bool{
	"sudo": true, "doas": true, "env": true, "nohup": true, "time": true, "nice": true,
	"ionice": true, "command": true, "builtin": true, "exec": true, "xargs": true,
	"timeout": true, "stdbuf": true, "setsid": true, "chroot": true, "watch": true,
}

// unwrap returns the command run by a wrapper such as `sudo -u x rm -rf y`,
// skipping its options and VAR=value assignments. Option values are not
// known, so a separate option argument may be taken for the command; that
// errs on the side of more commands for the policy to look at.
func unwrap(argv []string) []string {
	if len(argv) < 2 || !wrappers[filepath.Base(argv[0])] {
		return nil
	}
	name := filepath.Base(argv[0])
	rest := argv[1:]
	for len(rest) > 0 {
		a := rest[0]
		switch {
		case a == "--":
			rest = rest[1:]
			return nonEmptyArgv(rest)
		case strings.HasPrefix(a, "-"):
			rest = rest[1:]
			if name == "sudo" && (a == "-u" || a == "-g" || a == "-C" || a == "-D") && len(rest) > 0 {
				rest = rest[1:]
			}
		case name == "env" && strings.Contains(a, "="):
			rest = rest[1:]
		case name == "timeout" && len(a) > 0 && a[0] >= '0' && a[0] <= '9':
			rest = rest[1:]
		case name == "chroot":
			rest = rest[1:]
			return nonEmptyArgv(rest)
		default:
			return rest
		}
	}
	return nil
}

func nonEmptyArgv(a []string) []string {
	if len(a) == 0 {
		return nil
	}
	return a
}

// shellC finds the script of `bash -c SCRIPT`, `sh -c`, also behind sudo/env.
func shellC(argv []string) (string, bool) {
	for i := 0; i < len(argv); i++ {
		switch filepath.Base(argv[i]) {
		case "bash", "sh", "zsh", "dash":
			for j := i + 1; j < len(argv)-1; j++ {
				a := argv[j]
				if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "c") {
					return argv[j+1], true
				}
			}
			return "", false
		}
	}
	return "", false
}

func word(w *syntax.Word) string {
	var b strings.Builder
	for _, p := range w.Parts {
		part(&b, p)
	}
	return b.String()
}

func part(b *strings.Builder, p syntax.WordPart) {
	switch p := p.(type) {
	case *syntax.Lit:
		b.WriteString(unescape(p.Value))
	case *syntax.SglQuoted:
		b.WriteString(p.Value)
	case *syntax.DblQuoted:
		for _, q := range p.Parts {
			part(b, q)
		}
	default:
		_ = syntax.NewPrinter().Print(b, p)
	}
}

// unescape removes backslash quoting from an unquoted literal (r\m -> rm).
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
