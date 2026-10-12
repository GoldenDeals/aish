package mermaid

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

var pieSlice = regexp.MustCompile(`^"([^"]*)"\s*:\s*([\d.]+)$`) // "Код" : 45

// pie is a pie chart drawn as bars in w cells: a slice a row, the largest the full width, with its share
// of the whole and, under showData, its value.
func pie(src string, w int) string {
	var title string
	var names []string
	var values []float64
	data, total, top := false, 0.0, 0.0
	for i, l := range strings.Split(strings.TrimSpace(src), "\n") {
		l = strings.TrimSpace(l)
		if i == 0 { // pie [showData] [title …]
			l, data = strings.CutPrefix(strings.TrimSpace(strings.TrimPrefix(l, "pie")), "showData")
			l = strings.TrimSpace(l)
		}
		if t, ok := strings.CutPrefix(l, "title"); ok {
			title = strings.TrimSpace(t)
		} else if m := pieSlice.FindStringSubmatch(l); m != nil {
			if v, err := strconv.ParseFloat(m[2], 64); err == nil && v > 0 {
				names, values = append(names, m[1]), append(values, v)
				total, top = total+v, max(top, v)
			}
		}
	}
	if len(values) == 0 {
		return ""
	}

	tails := make([]string, len(values))
	nw, tw := 0, 0
	for i, v := range values {
		tails[i] = fmt.Sprintf("%5.1f%%", v/total*100)
		if data {
			tails[i] += " (" + strconv.FormatFloat(v, 'f', -1, 64) + ")"
		}
		nw, tw = max(nw, ansi.StringWidth(names[i])), max(tw, len(tails[i]))
	}
	bar := max(w-nw-tw-2, 1)
	var b strings.Builder
	if title != "" {
		b.WriteString(title + "\n")
	}
	for i, v := range values {
		n := int(math.Round(v / top * float64(bar)))
		fmt.Fprintf(&b, "%s%s %s%s %s\n", names[i], strings.Repeat(" ", nw-ansi.StringWidth(names[i])),
			strings.Repeat("█", n), strings.Repeat(" ", bar-n), tails[i])
	}
	return b.String()
}
