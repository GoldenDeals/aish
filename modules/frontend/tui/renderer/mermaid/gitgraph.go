package mermaid

import (
	"cmp"
	"regexp"
	"strings"

	uv "github.com/charmbracelet/ultraviolet"

	"github.com/GoldenDeals/aish-core/modules/frontend/tui/renderer"
)

var (
	gitAttr   = regexp.MustCompile(`(\w+):\s*(?:"([^"]*)"|(\S+))`) // id: "init", type: HIGHLIGHT
	gitCommit = map[string]string{"REVERSE": "✕", "HIGHLIGHT": "■"}
)

// gitGraph is a git graph drawn as git log --graph draws one, oldest first: a row a commit, a new branch
// or a merge, and a column a branch, taken by the next one once the branch is done.
func gitGraph(src string) string {
	type row struct {
		at, from     int // the branch the row is on, and the one it branches from or merges, or -1
		glyph, label string
	}
	names, cur := map[string]int{"main": 0}, 0
	var rows []row
	for _, l := range strings.Split(strings.TrimSpace(src), "\n")[1:] { // past the header
		f := strings.Fields(l)
		if len(f) == 0 {
			continue
		}
		attrs := map[string]string{}
		for _, m := range gitAttr.FindAllStringSubmatch(l, -1) {
			attrs[m[1]] = m[2] + m[3]
		}
		if attrs["tag"] != "" {
			attrs["tag"] = " (" + attrs["tag"] + ")"
		}
		arg := ""
		if len(f) > 1 {
			arg = strings.Trim(f[1], `"`)
		}
		b, known := names[arg]
		switch f[0] {
		case "commit", "cherry-pick":
			label := attrs["id"]
			if f[0] == "cherry-pick" {
				label = "cherry-pick " + label
			}
			rows = append(rows, row{cur, -1, cmp.Or(gitCommit[attrs["type"]], "●"), label + attrs["tag"]})
		case "branch":
			if known || arg == "" {
				return ""
			}
			names[arg] = len(names)
			rows = append(rows, row{len(names) - 1, cur, "┐", renderer.Styled(uv.Style{Attrs: uv.AttrBold}, arg)})
			cur = len(names) - 1
		case "checkout", "switch":
			if !known {
				return ""
			}
			cur = b
		case "merge":
			if !known {
				return ""
			}
			rows = append(rows, row{cur, b, cmp.Or(gitCommit[attrs["type"]], "●"), cmp.Or(attrs["id"], "merge "+arg) + attrs["tag"]})
		}
	}
	if len(rows) == 0 {
		return ""
	}

	n := len(names)
	first, last := make([]int, n), make([]int, n) // the rows a branch is drawn in
	for b := range first {
		first[b] = -1
	}
	for i, r := range rows {
		for _, b := range []int{r.at, r.from} {
			if b >= 0 {
				if first[b] < 0 {
					first[b] = i
				}
				last[b] = i
			}
		}
	}
	lane, width := make([]int, n), 1
	for b := 1; b < n; b++ {
		for l := 0; ; l++ {
			free := true
			for c := range b {
				if lane[c] == l && first[c] >= 0 && last[c] >= first[b] {
					free = false
				}
			}
			if free {
				lane[b], width = l, max(width, l+1)
				break
			}
		}
	}

	var out strings.Builder
	for i, r := range rows {
		lo, hi := lane[r.at], lane[r.at]
		if r.from >= 0 {
			lo, hi = min(lo, lane[r.from]), max(hi, lane[r.from])
		}
		glyphs := make([]string, width)
		for l := range glyphs {
			glyphs[l] = " "
		}
		for b := range n { // the branches going on past the row
			if first[b] < i && i < last[b] {
				glyphs[lane[b]] = "│"
			}
		}
		for l := lo + 1; l < hi; l++ { // crossed by the row's link
			if glyphs[l] == "│" {
				glyphs[l] = "┼"
			} else {
				glyphs[l] = "─"
			}
		}
		if r.from >= 0 {
			switch left, on := lane[r.from] < lane[r.at], last[r.from] > i; {
			case left && on:
				glyphs[lane[r.from]] = "├"
			case left:
				glyphs[lane[r.from]] = "└"
			case on:
				glyphs[lane[r.from]] = "┤"
			default:
				glyphs[lane[r.from]] = "┘"
			}
		}
		glyphs[lane[r.at]] = r.glyph
		if r.glyph == "┐" && lane[r.at] < lane[r.from] {
			glyphs[lane[r.at]] = "┌"
		}
		for l, g := range glyphs {
			out.WriteString(g)
			if l >= lo && l < hi {
				out.WriteString("──")
			} else {
				out.WriteString("  ")
			}
		}
		out.WriteString(strings.TrimSpace(r.label) + "\n")
	}
	return out.String()
}
