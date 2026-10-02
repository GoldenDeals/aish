// Package markdown renders the assistant's markdown for the terminal as it
// streams. The line being received is shown raw; once it is complete it is
// replaced by its rendering. Tables are shown when they end.
package markdown

import (
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/mattn/go-runewidth"
)

const (
	reset     = "\x1b[0m"
	codeColor = "\x1b[36m"
	linkColor = "\x1b[34m"
	dim       = "\x1b[2m"
)

// Writer renders markdown written to it onto a terminal.
type Writer struct {
	w    io.Writer
	size func() (cols, rows int)

	line   []byte // the line being received
	shown  int    // bytes of line shown raw
	frozen bool   // the raw preview got too tall to erase: line stays raw

	fence string // the open code fence, if any
	lexer chroma.Lexer
	code  strings.Builder // the code block so far
	style *chroma.Style
	table []string
	wrote bool
}

// New returns a Writer onto w; size reports the terminal size. style names
// the chroma style for code blocks.
func New(w io.Writer, size func() (cols, rows int), style string) *Writer {
	st := styles.Get(style)
	if st == nil {
		st = styles.Fallback
	}
	return &Writer{w: w, size: size, style: st}
}

func (m *Writer) Write(p []byte) (int, error) {
	for _, c := range p {
		if c == '\n' {
			m.complete()
			continue
		}
		if c == '\t' {
			m.line = append(m.line, "    "...)
		} else if c != '\r' {
			m.line = append(m.line, c)
		}
	}
	m.preview()
	return len(p), nil
}

// Flush renders what is left and ends at the start of a line.
func (m *Writer) Flush() {
	if len(m.line) > 0 {
		m.complete()
	}
	if len(m.table) > 0 {
		m.out(m.renderTable())
	}
	m.fence = ""
}

func (m *Writer) out(s string) {
	if s != "" {
		io.WriteString(m.w, s)
		m.wrote = true
	}
}

// preview shows the incomplete line raw. Table rows wait for the table.
func (m *Writer) preview() {
	if m.frozen || m.shown == len(m.line) || m.fence == "" && strings.HasPrefix(strings.TrimLeft(string(m.line), " "), "|") {
		return
	}
	cols, rows := m.size()
	if lineRows(string(m.line), cols) >= rows-1 {
		m.frozen = true
	}
	m.out(string(m.line[m.shown:]))
	m.shown = len(m.line)
}

func (m *Writer) complete() {
	raw := string(m.line)
	if m.shown > 0 && !m.frozen {
		cols, _ := m.size()
		up := lineRows(raw[:m.shown], cols) - 1
		erase := "\r"
		if up > 0 {
			erase += fmt.Sprintf("\x1b[%dA", up)
		}
		m.out(erase + "\x1b[J")
	}
	rendered := m.render(raw)
	if m.frozen {
		rendered = raw[m.shown:] + "\n"
	}
	m.out(rendered)
	m.line, m.shown, m.frozen = m.line[:0], 0, false
}

func lineRows(s string, cols int) int {
	w := runewidth.StringWidth(s)
	if w == 0 || cols <= 0 {
		return 1
	}
	return int(math.Ceil(float64(w) / float64(cols)))
}

var (
	fenceRe   = regexp.MustCompile("^\\s*(```+|~~~+)\\s*([^\\s`]*)")
	headingRe = regexp.MustCompile(`^(#{1,6})\s+(.*?)(\s+#+)?\s*$`)
	ruleRe    = regexp.MustCompile(`^\s*(?:(?:-\s*){3,}|(?:\*\s*){3,}|(?:_\s*){3,})$`)
	quoteRe   = regexp.MustCompile(`^\s*>\s?(.*)$`)
	listRe    = regexp.MustCompile(`^(\s*)([-*+]|\d+[.)])\s+(.*)$`)
	taskRe    = regexp.MustCompile(`^\[([ xX])\]\s+`)
)

// render returns the rendering of one complete line, with its newline.
func (m *Writer) render(l string) string {
	if m.fence != "" {
		if f := fenceRe.FindStringSubmatch(l); f != nil && f[2] == "" && strings.HasPrefix(f[1], m.fence) {
			m.fence = ""
			return ""
		}
		return m.codeLine(l) + "\n"
	}
	var s string
	if strings.HasPrefix(strings.TrimLeft(l, " "), "|") {
		m.table = append(m.table, l)
		return ""
	}
	if len(m.table) > 0 {
		s = m.renderTable()
	}
	cols, _ := m.size()
	switch {
	case fenceRe.MatchString(l):
		f := fenceRe.FindStringSubmatch(l)
		m.fence = f[1]
		m.code.Reset()
		m.lexer = nil
		if f[2] != "" {
			m.lexer = lexers.Get(f[2])
		}
		return s
	case strings.TrimSpace(l) == "":
		return s + "\n"
	case headingRe.MatchString(l):
		h := headingRe.FindStringSubmatch(l)
		st := "\x1b[1m"
		switch len(h[1]) {
		case 1:
			st = "\x1b[1;4m"
		case 2:
		default:
			st = "\x1b[1;3m"
		}
		return s + wrap(inline(h[2], st), "", "", cols) + "\n"
	case ruleRe.MatchString(l):
		return s + dim + strings.Repeat("─", min(cols, 80)) + reset + "\n"
	case quoteRe.MatchString(l):
		q := quoteRe.FindStringSubmatch(l)[1]
		gutter := dim + "│ " + reset
		return s + wrap(inline(q, "\x1b[3m"), gutter, gutter, cols) + "\n"
	case listRe.MatchString(l):
		f := listRe.FindStringSubmatch(l)
		indent, marker, text := f[1], f[2], f[3]
		bullet := marker + " "
		if !strings.ContainsAny(marker, ".)") {
			bullet = []string{"• ", "◦ ", "▪ "}[min(len(indent)/2, 2)]
		}
		if t := taskRe.FindStringSubmatch(text); t != nil {
			text = text[len(t[0]):]
			bullet += "☐ "
			if t[1] != " " {
				bullet = bullet[:len(bullet)-len("☐ ")] + "☑ "
			}
		}
		first := indent + bullet
		return s + wrap(inline(text, ""), first, strings.Repeat(" ", runewidth.StringWidth(first)), cols) + "\n"
	}
	trimmed := strings.TrimLeft(l, " ")
	indent := l[:len(l)-len(trimmed)]
	return s + wrap(inline(trimmed, ""), indent, indent, cols) + "\n"
}

// codeLine highlights the last line of the code block: the whole block is
// lexed again so that strings and comments spanning lines come out right.
func (m *Writer) codeLine(l string) string {
	if m.lexer == nil {
		return codeColor + l + reset
	}
	m.code.WriteString(l)
	m.code.WriteByte('\n')
	it, err := chroma.Coalesce(m.lexer).Tokenise(nil, m.code.String())
	if err != nil {
		return l
	}
	var lines [][]chroma.Token
	cur := []chroma.Token{}
	for _, t := range it.Tokens() {
		parts := strings.Split(t.Value, "\n")
		for i, part := range parts {
			if i > 0 {
				lines = append(lines, cur)
				cur = []chroma.Token{}
			}
			if part != "" {
				cur = append(cur, chroma.Token{Type: t.Type, Value: part})
			}
		}
	}
	if len(cur) > 0 {
		lines = append(lines, cur)
	}
	if len(lines) == 0 {
		return ""
	}
	// Plain text keeps the terminal's colour, whatever the style's background.
	text := m.style.Get(chroma.Text).Colour
	var b strings.Builder
	for _, t := range lines[len(lines)-1] {
		e := m.style.Get(t.Type)
		if e.Colour == text {
			e.Colour = 0
		}
		b.WriteString(sgr(e) + t.Value + reset)
	}
	return b.String()
}

var trueColor = os.Getenv("COLORTERM") == "truecolor" || os.Getenv("COLORTERM") == "24bit"

func sgr(e chroma.StyleEntry) string {
	var codes []string
	if e.Bold == chroma.Yes {
		codes = append(codes, "1")
	}
	if e.Italic == chroma.Yes {
		codes = append(codes, "3")
	}
	if e.Underline == chroma.Yes {
		codes = append(codes, "4")
	}
	if c := e.Colour; c.IsSet() {
		if trueColor {
			codes = append(codes, fmt.Sprintf("38;2;%d;%d;%d", c.Red(), c.Green(), c.Blue()))
		} else {
			q := func(v uint8) int { return (int(v)*5 + 127) / 255 }
			codes = append(codes, fmt.Sprintf("38;5;%d", 16+36*q(c.Red())+6*q(c.Green())+q(c.Blue())))
		}
	}
	if len(codes) == 0 {
		return ""
	}
	return "\x1b[" + strings.Join(codes, ";") + "m"
}

// renderTable draws the buffered table with box characters, or leaves it
// as text when it does not fit the terminal.
func (m *Writer) renderTable() string {
	rows := m.table
	m.table = nil
	var cells [][]string
	var align []byte
	header := false
	for i, r := range rows {
		cs := splitRow(r)
		if i == 1 && isSeparator(cs) {
			header = true
			for _, c := range cs {
				c = strings.TrimSpace(c)
				switch {
				case strings.HasPrefix(c, ":") && strings.HasSuffix(c, ":"):
					align = append(align, 'c')
				case strings.HasSuffix(c, ":"):
					align = append(align, 'r')
				default:
					align = append(align, 'l')
				}
			}
			continue
		}
		for j := range cs {
			st := ""
			if i == 0 {
				st = "\x1b[1m"
			}
			cs[j] = inline(strings.TrimSpace(cs[j]), st)
		}
		cells = append(cells, cs)
	}
	n := 0
	for _, r := range cells {
		n = max(n, len(r))
	}
	widths := make([]int, n)
	for _, r := range cells {
		for j, c := range r {
			widths[j] = max(widths[j], width(c))
		}
	}
	cols, _ := m.size()
	// Narrow the widest columns until the table fits; cells then wrap.
	room := cols - 1 - 3*n
	for sum(widths) > room && room >= 3*n {
		k := 0
		for j, w := range widths {
			if w > widths[k] {
				k = j
			}
		}
		widths[k]--
	}
	if n == 0 || sum(widths) > room {
		var b strings.Builder
		for _, r := range rows {
			b.WriteString(inline(r, "") + "\n")
		}
		return b.String()
	}
	border := func(l, mid, r string) string {
		var b strings.Builder
		b.WriteString(dim + l)
		for j, w := range widths {
			if j > 0 {
				b.WriteString(mid)
			}
			b.WriteString(strings.Repeat("─", w+2))
		}
		return b.String() + r + reset + "\n"
	}
	var b strings.Builder
	b.WriteString(border("┌", "┬", "┐"))
	for i, r := range cells {
		lines := make([][]string, len(widths))
		height := 1
		for j, w := range widths {
			if j < len(r) {
				lines[j] = cellLines(r[j], w)
			}
			height = max(height, len(lines[j]))
		}
		for y := 0; y < height; y++ {
			b.WriteString(dim + "│" + reset)
			for j := range widths {
				c := ""
				if y < len(lines[j]) {
					c = lines[j][y]
				}
				pad := widths[j] - width(c)
				a := byte('l')
				if j < len(align) {
					a = align[j]
				}
				left := 0
				switch a {
				case 'r':
					left = pad
				case 'c':
					left = pad / 2
				}
				b.WriteString(" " + strings.Repeat(" ", left) + c + reset + strings.Repeat(" ", pad-left) + " " + dim + "│" + reset)
			}
			b.WriteString("\n")
		}
		if i == 0 && header && len(cells) > 1 {
			b.WriteString(border("├", "┼", "┤"))
		}
	}
	b.WriteString(border("└", "┴", "┘"))
	return b.String()
}

func sum(ws []int) int {
	t := 0
	for _, w := range ws {
		t += w
	}
	return t
}

// cellLines wraps styled text to w columns, splitting words that do not
// fit. Each line starts with the styles in effect where it begins.
func cellLines(s string, w int) []string {
	var out []string
	var styles string
	for _, l := range strings.Split(wrap(s, "", "", w), "\n") {
		for {
			line, rest := cut(l, w)
			out = append(out, styles+line)
			styles += strings.Join(ansiRe.FindAllString(line, -1), "")
			if rest == "" {
				break
			}
			l = rest
		}
	}
	return out
}

// cut splits styled text after w columns.
func cut(s string, w int) (string, string) {
	col := 0
	for i := 0; i < len(s); {
		if loc := ansiRe.FindStringIndex(s[i:]); loc != nil && loc[0] == 0 {
			i += loc[1]
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if col+runewidth.RuneWidth(r) > w {
			return s[:i], s[i:]
		}
		col += runewidth.RuneWidth(r)
		i += size
	}
	return s, ""
}

func splitRow(r string) []string {
	r = strings.TrimSpace(r)
	r = strings.TrimPrefix(r, "|")
	if strings.HasSuffix(r, "|") && !strings.HasSuffix(r, `\|`) {
		r = r[:len(r)-1]
	}
	var cells []string
	var cur strings.Builder
	code := false
	for i := 0; i < len(r); i++ {
		switch {
		case r[i] == '\\' && i+1 < len(r) && r[i+1] == '|':
			cur.WriteByte('|')
			i++
		case r[i] == '`':
			code = !code
			cur.WriteByte('`')
		case r[i] == '|' && !code:
			cells = append(cells, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(r[i])
		}
	}
	return append(cells, cur.String())
}

func isSeparator(cs []string) bool {
	for _, c := range cs {
		c = strings.TrimSpace(c)
		if c == "" || strings.Trim(c, ":-") != "" || !strings.Contains(c, "-") {
			return false
		}
	}
	return true
}

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

func width(s string) int {
	return runewidth.StringWidth(ansiRe.ReplaceAllString(s, ""))
}

// wrap breaks styled text into lines of at most cols columns, starting with
// first and continuing with rest.
func wrap(s, first, rest string, cols int) string {
	if cols <= 0 {
		return first + s
	}
	var b strings.Builder
	b.WriteString(first)
	col := width(first)
	start := true
	for _, word := range strings.Split(s, " ") {
		w := width(word)
		if !start && col+1+w > cols {
			b.WriteString("\n" + rest)
			col = width(rest)
			start = true
		}
		if !start {
			b.WriteByte(' ')
			col++
		}
		b.WriteString(word)
		col += w
		start = false
	}
	return b.String()
}

// inline renders emphasis, code spans and links; base is the style of the
// surrounding text.
func inline(s, base string) string {
	var b strings.Builder
	var bold, italic, strike bool
	style := func() string {
		st := reset + base
		if bold {
			st += "\x1b[1m"
		}
		if italic {
			st += "\x1b[3m"
		}
		if strike {
			st += "\x1b[9m"
		}
		return st
	}
	b.WriteString(base)
	alnum := func(i int) bool {
		if i < 0 || i >= len(s) {
			return false
		}
		c := s[i]
		return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c >= 0x80
	}
	space := func(i int) bool { return i < 0 || i >= len(s) || s[i] == ' ' }
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s) && strings.IndexByte("\\`*_{}[]()#+-.!|~<>", s[i+1]) >= 0:
			b.WriteByte(s[i+1])
			i++
			continue
		case c == '`':
			n := 1
			for i+n < len(s) && s[i+n] == '`' {
				n++
			}
			ticks := s[i : i+n]
			if j := strings.Index(s[i+n:], ticks); j >= 0 {
				code := strings.TrimSpace(s[i+n : i+n+j])
				b.WriteString(reset + codeColor + code + style())
				i += n + j + n - 1
				continue
			}
		case (c == '*' || c == '_') && i+1 < len(s) && s[i+1] == c:
			d := s[i : i+2]
			if bold && !space(i-1) {
				bold = false
				b.WriteString(style())
				i++
				continue
			}
			if !bold && !space(i+2) && (c == '*' || !alnum(i-1)) && strings.Contains(s[i+2:], d) {
				bold = true
				b.WriteString(style())
				i++
				continue
			}
		case c == '*' || c == '_':
			if italic && !space(i-1) && (c == '*' || !alnum(i+1)) {
				italic = false
				b.WriteString(style())
				continue
			}
			if !italic && !space(i+1) && (c == '*' || !alnum(i-1)) && strings.IndexByte(s[i+1:], c) >= 0 {
				italic = true
				b.WriteString(style())
				continue
			}
		case c == '~' && i+1 < len(s) && s[i+1] == '~':
			if strike || strings.Contains(s[i+2:], "~~") {
				strike = !strike
				b.WriteString(style())
				i++
				continue
			}
		case c == '[' || c == '!' && i+1 < len(s) && s[i+1] == '[':
			if text, url, n, ok := link(s[i:]); ok {
				b.WriteString(reset + linkColor + inline(text, linkColor) + reset)
				if url != text && url != "" {
					b.WriteString(dim + " (" + url + ")" + reset)
				}
				b.WriteString(style())
				i += n - 1
				continue
			}
		case c == '<':
			if j := strings.IndexByte(s[i:], '>'); j > 0 && (strings.HasPrefix(s[i+1:], "http://") || strings.HasPrefix(s[i+1:], "https://")) {
				b.WriteString(reset + linkColor + s[i+1:i+j] + style())
				i += j
				continue
			}
		}
		b.WriteByte(c)
	}
	if bold || italic || strike || base != "" {
		b.WriteString(reset)
	}
	return b.String()
}

// link parses [text](url) or ![alt](url) at the start of s.
func link(s string) (text, url string, n int, ok bool) {
	off := 0
	if s[0] == '!' {
		off = 1
	}
	depth := 0
	for i := off; i < len(s); i++ {
		switch s[i] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				if i+1 >= len(s) || s[i+1] != '(' {
					return "", "", 0, false
				}
				j := strings.IndexByte(s[i+2:], ')')
				if j < 0 {
					return "", "", 0, false
				}
				return s[off+1 : i], strings.TrimSpace(s[i+2 : i+2+j]), i + 3 + j, true
			}
		}
	}
	return "", "", 0, false
}
