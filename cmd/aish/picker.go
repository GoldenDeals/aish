package main

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
	"golang.org/x/term"

	"github.com/inebotov/aish/internal/session"
)

// picker is the full-screen list `aish resume` shows without an argument.
type picker struct {
	dir  string
	list []session.Info
	cur  string // the session of this shell, inside aish
	def  string // the profile config.toml selects, which goes unnamed
	sel  int
	top  int
	w, h int

	editing bool
	edit    []rune
	// deleting: the next key is y to delete the session chosen, or not.
	deleting bool
	msg      string
}

func pickSession(dir string, list []session.Info, cur, def string) (session.Info, bool, error) {
	in, out := int(os.Stdin.Fd()), int(os.Stdout.Fd())
	old, err := term.MakeRaw(in)
	if err != nil {
		return session.Info{}, false, err
	}
	defer term.Restore(in, old)
	fmt.Print("\x1b[?1049h\x1b[?25l")
	defer fmt.Print("\x1b[?25h\x1b[?1049l")

	p := &picker{dir: dir, list: list, cur: cur, def: def}
	buf := make([]byte, 256)
	for {
		p.w, p.h, err = term.GetSize(out)
		if err != nil || p.w < 20 || p.h < 6 {
			p.w, p.h = 80, 24
		}
		fmt.Print(p.render())
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return session.Info{}, false, err
		}
		for _, k := range keys(buf[:n]) {
			if done, ok := p.key(k); done {
				if !ok { // the list may be empty, all deleted
					return session.Info{}, false, nil
				}
				return p.list[p.sel], true, nil
			}
		}
	}
}

// keys splits terminal input into keys: escape sequences whole, a lone
// Esc as itself.
func keys(b []byte) []string {
	var ks []string
	for len(b) > 0 {
		if b[0] == 0x1b && len(b) > 2 && (b[1] == '[' || b[1] == 'O') {
			i := 2
			for i < len(b) && (b[i] < 0x40 || b[i] > 0x7e) {
				i++
			}
			i = min(i+1, len(b))
			ks, b = append(ks, string(b[:i])), b[i:]
			continue
		}
		_, size := utf8.DecodeRune(b)
		ks, b = append(ks, string(b[:size])), b[size:]
	}
	return ks
}

// key handles one key; done means the picker closes, ok that a session
// was chosen.
func (p *picker) key(k string) (done, ok bool) {
	if p.editing {
		switch k {
		case "\r":
			id := p.list[p.sel].ID
			if err := session.Rename(p.dir, id, string(p.edit)); err != nil {
				p.msg = err.Error()
				return false, false
			}
			p.editing, p.msg = false, ""
			p.reload(id)
		case "\x1b", "\x03":
			p.editing, p.msg = false, ""
		case "\x7f", "\x08":
			if len(p.edit) > 0 {
				p.edit = p.edit[:len(p.edit)-1]
			}
		case "\x15":
			p.edit = nil
		default:
			if r, _ := utf8.DecodeRuneInString(k); len(k) == utf8.RuneLen(r) && unicode.IsPrint(r) {
				p.edit = append(p.edit, r)
			}
		}
		return false, false
	}
	p.msg = ""
	if p.deleting {
		p.deleting = false
		if k != "y" {
			return false, false
		}
		if err := session.Remove(p.dir, p.list[p.sel].ID); err != nil {
			p.msg = err.Error()
			return false, false
		}
		p.list = slices.Delete(p.list, p.sel, p.sel+1)
		p.sel = min(p.sel, len(p.list)-1)
		return len(p.list) == 0, false
	}
	page := p.rows()
	switch k {
	case "\x1b[A", "\x1bOA", "k", "\x10":
		p.sel--
	case "\x1b[B", "\x1bOB", "j", "\x0e":
		p.sel++
	case "\x1b[5~":
		p.sel -= page
	case "\x1b[6~":
		p.sel += page
	case "\x1b[H", "\x1b[1~", "g":
		p.sel = 0
	case "\x1b[F", "\x1b[4~", "G":
		p.sel = len(p.list) - 1
	case "r":
		p.editing = true
		p.edit = []rune(p.list[p.sel].Name)
	case "d":
		switch i := p.list[p.sel]; {
		case i.ID == p.cur:
			p.msg = "this shell is in this session; aish clear leaves it"
		case i.Open:
			p.msg = "open in another aish"
		default:
			p.deleting = true
		}
	case "\r":
		i := p.list[p.sel]
		switch {
		case i.ID == p.cur:
			p.msg = "this shell is in this session already"
		case i.Open:
			p.msg = "open in another aish"
		default:
			return true, true
		}
	case "q", "\x1b", "\x03", "\x04":
		return true, false
	}
	p.sel = max(0, min(p.sel, len(p.list)-1))
	return false, false
}

func (p *picker) reload(id string) {
	list, err := session.List(p.dir)
	if err != nil || len(list) == 0 {
		return
	}
	p.list, p.sel = list, 0
	for n, i := range list {
		if i.ID == id {
			p.sel = n
		}
	}
}

// rows is how many sessions fit: two lines each, below the title and above
// the status line.
func (p *picker) rows() int { return max(1, (p.h-3)/2) }

func (p *picker) render() string {
	var b strings.Builder
	b.WriteString("\x1b[H\x1b[2J")
	fit := func(s string) string { return runewidth.Truncate(s, p.w-1, "…") }
	fmt.Fprintf(&b, "\x1b[1msessions\x1b[0m\x1b[2m%s\x1b[0m\r\n\r\n",
		runewidth.Truncate("   ↑↓ choose · enter resume · r rename · d delete · q quit", p.w-9, "…"))

	rows := p.rows()
	if p.sel < p.top {
		p.top = p.sel
	} else if p.sel >= p.top+rows {
		p.top = p.sel - rows + 1
	}
	for n := p.top; n < len(p.list) && n < p.top+rows; n++ {
		i := p.list[n]
		mark, style := "  ", ""
		if n == p.sel {
			mark, style = "▸ ", "\x1b[1;36m"
		}
		where := ago(i.Modified)
		if i.Cwd != "" {
			where += "  " + home(i.Cwd)
		}
		if m := sessionModel(i, p.def); m != "" {
			where += "  " + m
		}
		switch {
		case i.ID == p.cur:
			where += "  [this shell]"
		case i.Open:
			where += "  [open]"
		}
		title := i.Title()
		if n == p.sel && p.editing {
			title = string(p.edit) + "█"
		}
		head := fit(mark + title)
		rest := p.w - 1 - runewidth.StringWidth(head)
		pad := max(1, 34-runewidth.StringWidth(head))
		tail := ""
		if rest > pad {
			tail = strings.Repeat(" ", pad) + runewidth.Truncate(where, rest-pad, "…")
		}
		fmt.Fprintf(&b, "%s%s\x1b[0m\x1b[2m%s\x1b[0m\r\n", style, head, tail)

		var about []string
		if i.Name != "" {
			about = append(about, i.ID)
		}
		about = append(about, fmt.Sprintf("%d requests", i.Requests))
		if i.Last != "" {
			last, _, _ := strings.Cut(i.Last, "\n")
			about = append(about, "“"+last+"”")
		}
		fmt.Fprintf(&b, "\x1b[2m%s\x1b[0m\r\n", fit("    "+strings.Join(about, " · ")))
	}

	fmt.Fprintf(&b, "\x1b[%dH", p.h)
	switch {
	case p.editing:
		fmt.Fprintf(&b, "\x1b[2m%s\x1b[0m", fit("rename: enter save · esc cancel · empty name removes it"))
	case p.deleting:
		fmt.Fprintf(&b, "\x1b[33m%s\x1b[0m", fit("delete "+p.list[p.sel].Title()+"? y deletes, any other key keeps it"))
	case p.msg != "":
		fmt.Fprintf(&b, "\x1b[33m%s\x1b[0m", fit(p.msg))
	case len(p.list) > rows:
		fmt.Fprintf(&b, "\x1b[2m%d/%d\x1b[0m", p.sel+1, len(p.list))
	}
	return b.String()
}
