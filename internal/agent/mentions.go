package agent

import (
	"bufio"
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/inebotov/aish/internal/session"
)

const (
	maxMentionFiles = 50     // files one request may attach
	maxMentionBytes = 200000 // all attached files together
)

// mentionRe finds @path, @glob and @"path with spaces" at the start of a word,
// so that user@host is not a mention.
var mentionRe = regexp.MustCompile(`(?:^|\s)@(?:"([^"]+)"|(\S+))`)

// parseMentions returns the paths and patterns mentioned in text, in order
// and without repeats.
func parseMentions(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range mentionRe.FindAllStringSubmatch(text, -1) {
		p := m[1]
		if p == "" {
			// Punctuation after a mention belongs to the sentence: "see @a.go."
			p = strings.TrimRight(m[2], ".,;:!?)]}»\"'")
		}
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// mentions reads the files text mentions as @path or @pattern. Each becomes a
// file entry; a mention that matches nothing becomes an error entry.
func mentions(text, cwd string) []session.Entry {
	var out []session.Entry
	seen := map[string]bool{}
	budget := maxMentionBytes
	for _, m := range parseMentions(text) {
		paths, err := expandMention(m, cwd)
		if err == nil && len(paths) == 0 {
			err = fmt.Errorf("no such file")
		}
		if err != nil {
			out = append(out, session.Entry{Kind: session.KindFile, Path: m, Text: err.Error(), IsError: true})
			continue
		}
		for _, p := range paths {
			if seen[p] {
				continue
			}
			seen[p] = true
			if len(seen) > maxMentionFiles {
				out = append(out, session.Entry{Kind: session.KindFile, Path: m,
					Text: fmt.Sprintf("more than %d files, the rest are not attached", maxMentionFiles), IsError: true})
				break
			}
			e := readMention(p, budget)
			budget -= len(e.Text)
			out = append(out, e)
		}
	}
	return out
}

// expandMention resolves one mention against cwd: a file or directory as is,
// a pattern with filepath.Match syntax plus ** for any number of directories.
func expandMention(m, cwd string) ([]string, error) {
	if m == "~" || strings.HasPrefix(m, "~/") {
		home, _ := os.UserHomeDir()
		m = home + m[1:]
	}
	if !filepath.IsAbs(m) {
		m = filepath.Join(cwd, m)
	}
	m = filepath.Clean(m)
	if !strings.ContainsAny(m, "*?[") {
		if _, err := os.Stat(m); err != nil {
			return nil, fmt.Errorf("no such file")
		}
		return []string{m}, nil
	}
	if _, err := filepath.Match(m, ""); err != nil {
		return nil, fmt.Errorf("bad pattern")
	}
	// Walk from the longest directory without wildcards.
	base := m
	for strings.ContainsAny(base, "*?[") {
		base = filepath.Dir(base)
	}
	pat := strings.Split(strings.TrimPrefix(m[len(base):], string(filepath.Separator)), string(filepath.Separator))
	var out []string
	filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == base {
			return nil
		}
		rel, _ := filepath.Rel(base, p)
		segs := strings.Split(rel, string(filepath.Separator))
		name := segs[len(segs)-1]
		if d.IsDir() {
			// Hidden directories and dependencies only when named explicitly.
			if (strings.HasPrefix(name, ".") || name == "node_modules") && !literalIn(pat, name) {
				return filepath.SkipDir
			}
			if !matchPrefix(pat, segs) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(pat[len(pat)-1], ".") {
			return nil
		}
		if d.Type().IsRegular() && matchSegs(pat, segs) || d.Type()&fs.ModeSymlink != 0 && matchSegs(pat, segs) && isRegular(p) {
			out = append(out, p)
		}
		return nil
	})
	sort.Strings(out)
	return out, nil
}

func isRegular(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

func literalIn(pat []string, name string) bool {
	for _, p := range pat {
		if p == name {
			return true
		}
	}
	return false
}

// matchSegs matches path segments against pattern segments, where "**"
// stands for any number of segments.
func matchSegs(pat, segs []string) bool {
	if len(pat) == 0 {
		return len(segs) == 0
	}
	if pat[0] == "**" {
		for i := 0; i <= len(segs); i++ {
			if matchSegs(pat[1:], segs[i:]) {
				return true
			}
		}
		return false
	}
	if len(segs) == 0 {
		return false
	}
	ok, _ := filepath.Match(pat[0], segs[0])
	return ok && matchSegs(pat[1:], segs[1:])
}

// matchPrefix tells whether a directory with these segments may contain a
// match.
func matchPrefix(pat, segs []string) bool {
	if len(segs) == 0 {
		return true
	}
	if len(pat) == 0 {
		return false
	}
	if pat[0] == "**" {
		return true
	}
	ok, _ := filepath.Match(pat[0], segs[0])
	return ok && matchPrefix(pat[1:], segs[1:])
}

// readMention reads a mentioned file with line numbers, as read_file shows
// it, or lists a directory. At most budget bytes are kept.
func readMention(p string, budget int) session.Entry {
	e := session.Entry{Kind: session.KindFile, Path: p}
	fail := func(msg string) session.Entry {
		e.Text, e.IsError = msg, true
		return e
	}
	st, err := os.Stat(p)
	if err != nil {
		return fail("no such file")
	}
	if st.IsDir() {
		ents, err := os.ReadDir(p)
		if err != nil {
			return fail(err.Error())
		}
		var b strings.Builder
		for _, d := range ents {
			b.WriteString(d.Name())
			if d.IsDir() {
				b.WriteByte('/')
			}
			b.WriteByte('\n')
		}
		e.About, e.Text = "directory", b.String()
		return e
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return fail(err.Error())
	}
	if bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
		return fail("binary file, not attached")
	}
	limit := min(maxInstructionBytes, max(budget, 0))
	if limit == 0 {
		return fail("too much attached already, use read_file")
	}
	var b strings.Builder
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	n := 0
	for sc.Scan() {
		n++
		line := sc.Text()
		if len(line) > 2000 {
			line = line[:2000] + "[...]"
		}
		if b.Len()+len(line) > limit {
			fmt.Fprintf(&b, "[truncated; continue with read_file offset=%d]\n", n)
			break
		}
		fmt.Fprintf(&b, "%6d\t%s\n", n, line)
	}
	if n == 0 {
		b.WriteString("(empty file)\n")
	}
	e.Text = b.String()
	return e
}

// mentionNote is the dim line shown under the request for one mention.
func mentionNote(e session.Entry, cwd string) string {
	p := e.Path
	if filepath.IsAbs(p) {
		if rel, err := filepath.Rel(cwd, p); err == nil && !strings.HasPrefix(rel, "..") {
			p = rel
		} else {
			p = tildePath(p)
		}
	}
	switch {
	case e.IsError:
		return "@" + p + ": " + e.Text
	case e.About == "directory":
		return "@" + p + "/"
	default:
		lines, more := strings.Count(e.Text, "\n"), ""
		if strings.HasSuffix(e.Text, "(empty file)\n") {
			lines = 0
		}
		if strings.Contains(e.Text, "\n[truncated; ") {
			lines, more = lines-1, ", truncated"
		}
		s := "s"
		if lines == 1 {
			s = ""
		}
		return fmt.Sprintf("@%s (%d line%s%s)", p, lines, s, more)
	}
}

func filesBlock(es []session.Entry) string {
	var b strings.Builder
	b.WriteString("<system-reminder>\nThe user mentioned these files with @ in the request below; " +
		"they were read for you, so do not read them again unless they change.\n")
	for _, e := range es {
		switch {
		case e.IsError:
			b.WriteString("\n@" + e.Path + ": " + e.Text + "\n")
		case e.About == "directory":
			b.WriteString("\nListing of directory " + e.Path + ":\n" + e.Text)
		default:
			b.WriteString("\nContents of " + e.Path + ":\n" + e.Text)
		}
	}
	b.WriteString("</system-reminder>")
	return b.String()
}
