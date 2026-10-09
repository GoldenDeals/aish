// Package docs holds the reference of aish, a file per topic, that
// README.md points to. Its only code is the test that keeps the links of
// README.md and of these files from leading nowhere when a section moves.
package docs

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// TestLinks checks every relative link of README.md and docs/*.md: the file
// it names exists, and its #anchor, in a Markdown file, is that of a heading
// there. Links with a scheme (https:, mailto:) are not followed.
func TestLinks(t *testing.T) {
	files, err := filepath.Glob("*.md")
	if err != nil {
		t.Fatal(err)
	}
	files = append([]string{"../README.md"}, files...)
	anchors := map[string]map[string]bool{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		ls := links(string(data))
		if f == "../README.md" && len(ls) == 0 {
			t.Errorf("%s: no links found: is links() broken?", f)
		}
		for _, l := range ls {
			if msg := check(f, l, anchors); msg != "" {
				t.Errorf("%s: link %q: %s", f, l, msg)
			}
		}
	}
}

// TestCheck makes sure a broken link does not pass for a good one.
func TestCheck(t *testing.T) {
	dir := t.TempDir()
	md := filepath.Join(dir, "a.md")
	text := "# Команда или вопрос?\n\n## Инструкции (CLAUDE.md)\n\n```sh\n# not a heading\n```\n\n" +
		"## `aish tool` и MCP-серверы\n\n## Скиллы\n\n## Скиллы\n"
	if err := os.WriteFile(md, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	for l, bad := range map[string]bool{
		"a.md": false,
		"a.md#команда-или-вопрос":                   false,
		"#инструкции-claudemd":                      false,
		"a.md#aish-tool-и-mcp-серверы":              false,
		"a.md#скиллы-1":                             false,
		"a.md#%D1%81%D0%BA%D0%B8%D0%BB%D0%BB%D1%8B": false,
		"https://example.com/nowhere":               false,
		"a.md#скиллы-2":                             true,
		"a.md#not-a-heading":                        true,
		"a.md#Команда-или-вопрос":                   true,
		"b.md":        true,
		"b.md#скиллы": true,
		"/etc/passwd": true,
	} {
		msg := check(md, l, map[string]map[string]bool{})
		if (msg != "") != bad {
			t.Errorf("check(%q) = %q, want broken %v", l, msg, bad)
		}
	}
}

// TestLinksFound makes sure links in code are not taken for links, and
// links over a line break or in a list item are.
func TestLinksFound(t *testing.T) {
	text := "See [one](a.md) and `[code](no.md)`, ``x `[c](no.md)` y``.\n" +
		"- a [two](b.md#x \"title\") and a span `that goes\non [c](no.md)` here\n\n" +
		"  ```md\n  [fenced](no.md)\n  ```\n\n[ref]: c.md\n"
	got := strings.Join(links(text), " ")
	if want := "a.md b.md#x c.md"; got != want {
		t.Errorf("links = %q, want %q", got, want)
	}
}

// check tells what is wrong with link target l of file from, "" if
// nothing; anchors caches the anchors of the files read.
func check(from, l string, anchors map[string]map[string]bool) string {
	if scheme.MatchString(l) {
		return ""
	}
	path, frag, _ := strings.Cut(l, "#")
	path, err := url.PathUnescape(path)
	if err != nil {
		return err.Error()
	}
	target := from
	if path != "" {
		if filepath.IsAbs(path) {
			return "not a relative path"
		}
		target = filepath.Join(filepath.Dir(from), path)
		if _, err := os.Stat(target); err != nil {
			return "no such file"
		}
	}
	if frag == "" || !strings.HasSuffix(target, ".md") {
		return ""
	}
	frag, err = url.PathUnescape(frag)
	if err != nil {
		return err.Error()
	}
	if anchors[target] == nil {
		data, err := os.ReadFile(target)
		if err != nil {
			return err.Error()
		}
		anchors[target] = headings(string(data))
	}
	if !anchors[target][frag] {
		return "no heading with this anchor in " + target
	}
	return ""
}

var (
	scheme   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)
	fence    = regexp.MustCompile("^(```+|~~~+)")
	inline   = regexp.MustCompile(`\]\(\s*<?([^)\s>]*)>?(?:\s+"[^"]*")?\s*\)`)
	refDef   = regexp.MustCompile(`(?m)^ {0,3}\[[^\]]+\]:\s*<?(\S+?)>?(?:\s|$)`)
	heading  = regexp.MustCompile(`^ {0,3}(#{1,6})(?:[ \t]+(.*?))?(?:[ \t]+#+)?[ \t]*$`)
	linkText = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)
)

// prose splits text into its paragraphs, leaving out fenced code blocks,
// where nothing is a link or a heading.
func prose(text string) []string {
	var paras []string
	var cur []string
	closer := ""
	flush := func() {
		if len(cur) > 0 {
			paras = append(paras, strings.Join(cur, "\n"))
			cur = nil
		}
	}
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimLeft(line, " ")
		if closer != "" {
			if strings.HasPrefix(trimmed, closer) && strings.Trim(trimmed, closer[:1]+" \t") == "" {
				closer = ""
			}
			continue
		}
		// A fence may stand indented in a list item: any indent opens one.
		if m := fence.FindStringSubmatch(trimmed); m != nil {
			flush()
			closer = m[1]
			continue
		}
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		cur = append(cur, line)
	}
	flush()
	return paras
}

// uncode drops the code spans of a paragraph: `[x](y)` there is text. A
// span may go on over a line break; a run of backticks with no match is
// text itself.
func uncode(p string) string {
	var b strings.Builder
	for i := 0; i < len(p); {
		if p[i] != '`' {
			b.WriteByte(p[i])
			i++
			continue
		}
		n := 0
		for i+n < len(p) && p[i+n] == '`' {
			n++
		}
		run := p[i : i+n]
		end := -1
		for j := i + n; j < len(p); {
			k := strings.Index(p[j:], run)
			if k < 0 {
				break
			}
			k += j
			m := 0
			for k+m < len(p) && p[k+m] == '`' {
				m++
			}
			if m == n {
				end = k
				break
			}
			j = k + m
		}
		if end < 0 {
			b.WriteString(run)
			i += n
			continue
		}
		b.WriteString(" ")
		i = end + n
	}
	return b.String()
}

// links lists the targets of the links of a Markdown text.
func links(text string) []string {
	var out []string
	for _, p := range prose(text) {
		p = uncode(p)
		for _, m := range inline.FindAllStringSubmatch(p, -1) {
			out = append(out, m[1])
		}
		for _, m := range refDef.FindAllStringSubmatch(p, -1) {
			out = append(out, m[1])
		}
	}
	return out
}

// headings gives the anchors GitHub makes of the ATX headings of a text: a
// repeated one gets -1, -2… after it.
func headings(text string) map[string]bool {
	out := map[string]bool{}
	seen := map[string]int{}
	for _, p := range prose(text) {
		for _, line := range strings.Split(p, "\n") {
			m := heading.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			a := slug(m[2])
			if n := seen[a]; n > 0 {
				seen[a]++
				a += "-" + strconv.Itoa(n)
			} else {
				seen[a] = 1
			}
			out[a] = true
		}
	}
	return out
}

// slug is the anchor of a heading as GitHub makes it: the text of the
// heading in lower case, without the marks of code and links, with all but
// letters, digits, marks, "_", "-" and spaces dropped and each space made
// "-". Cyrillic stays: «Команда или вопрос?» is команда-или-вопрос.
func slug(h string) string {
	h = linkText.ReplaceAllString(h, "$1")
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(h)) {
		switch {
		case r == ' ':
			b.WriteRune('-')
		case r == '-' || unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r) || unicode.Is(unicode.Pc, r):
			b.WriteRune(r)
		}
	}
	return b.String()
}
