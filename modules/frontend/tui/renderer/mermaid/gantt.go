package mermaid

import (
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/GoldenDeals/aish-core/modules/frontend/tui/renderer"
)

// strftime turns mermaid's axisFormat into a Go layout.
var strftime = strings.NewReplacer("%Y", "2006", "%y", "06", "%m", "01", "%d", "02", "%e", "_2",
	"%b", "Jan", "%B", "January", "%a", "Mon", "%A", "Monday", "%H", "15", "%M", "04")

// row is a task of a gantt chart, or a section: a row with no start.
type row struct {
	name, tags string // tags: done, active, crit, milestone
	start, end time.Time
}

// gantt is a gantt chart drawn in w cells: a row a section and a task, the task's bar on a time axis spanning
// the chart. Done tasks are ░, active ▓, critical red, milestones ◆.
// shortcut: dateFormat YYYY-MM-DD only and excludes ignored; another dateFormat leaves the source.
func gantt(src string, w int) string {
	title, axis := "", "%Y-%m-%d"
	var rows []row
	ids := map[string]row{}
	var prev time.Time                                                  // the end of the task before
	for _, l := range strings.Split(strings.TrimSpace(src), "\n")[1:] { // past the header
		l = strings.TrimSpace(l)
		key, value, _ := strings.Cut(l, " ")
		switch value = strings.TrimSpace(value); key {
		case "title":
			title = value
		case "dateFormat":
			if value != "YYYY-MM-DD" {
				return ""
			}
		case "axisFormat":
			axis = value
		case "section":
			rows = append(rows, row{name: value})
		case "excludes", "includes", "todayMarker", "tickInterval", "weekday", "inclusiveEndDates", "click":
		default:
			name, spec, ok := strings.Cut(l, ":")
			if !ok || strings.HasPrefix(l, "%%") {
				continue
			}
			t, id, ok := task(strings.TrimSpace(name), spec, prev, ids)
			if !ok {
				return ""
			}
			if id != "" {
				ids[id] = t
			}
			rows, prev = append(rows, t), t.end
		}
	}

	var lo, hi time.Time
	lw := 0 // the width of the names, tasks indented under their sections
	for _, r := range rows {
		nw := ansi.StringWidth(r.name)
		if !r.start.IsZero() {
			nw += 2
			if lo.IsZero() || r.start.Before(lo) {
				lo = r.start
			}
			if r.end.After(hi) {
				hi = r.end
			}
		}
		lw = max(lw, nw)
	}
	if lo.IsZero() {
		return ""
	}
	if !hi.After(lo) {
		hi = lo.Add(24 * time.Hour)
	}
	cols := max(w-lw-1, 10)
	x := func(t time.Time) int { // the column of t; a bar of the last moment still fits
		return int(math.Round(float64(t.Sub(lo)) / float64(hi.Sub(lo)) * float64(cols-1)))
	}

	var b strings.Builder
	if title != "" {
		b.WriteString(title + "\n")
	}
	ticks := strings.Repeat(" ", lw+1)
	for c := 0; ; {
		label := lo.Add(time.Duration(float64(hi.Sub(lo)) * float64(c) / float64(cols-1))).Format(strftime.Replace(axis))
		if c+ansi.StringWidth(label) > cols {
			break
		}
		ticks += label + "   "
		c += ansi.StringWidth(label) + 3
	}
	b.WriteString(strings.TrimRight(ticks, " ") + "\n")
	for _, r := range rows {
		if r.start.IsZero() {
			b.WriteString(r.name + "\n")
			continue
		}
		from := x(r.start)
		n := max(x(r.end)-from, 1)
		bar := strings.Repeat("█", n)
		switch {
		case strings.Contains(r.tags, "milestone"):
			bar = "◆"
		case strings.Contains(r.tags, "done"):
			bar = strings.Repeat("░", n)
		case strings.Contains(r.tags, "active"):
			bar = strings.Repeat("▓", n)
		}
		if strings.Contains(r.tags, "crit") {
			bar = renderer.Styled(uv.Style{Fg: ansi.Red}, bar)
		}
		b.WriteString("  " + r.name + strings.Repeat(" ", lw-2-ansi.StringWidth(r.name)+1+from) + bar + "\n")
	}
	return b.String()
}

// task is the gantt task name with spec: [tags,] [id,] [start,] end, where the start is a date or after ids
// (by default the end of the task before) and the end is a date, a length in days, weeks or hours, or until id.
func task(name, spec string, prev time.Time, ids map[string]row) (t row, id string, ok bool) {
	parts := strings.Split(spec, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	t = row{name: name, start: prev}
	for len(parts) > 1 && slices.Contains([]string{"done", "active", "crit", "milestone"}, parts[0]) {
		t.tags, parts = t.tags+" "+parts[0], parts[1:]
	}
	if len(parts) == 3 {
		id, parts = parts[0], parts[1:]
	}
	if len(parts) == 2 {
		if after, ok := strings.CutPrefix(parts[0], "after "); ok {
			t.start = time.Time{}
			for _, a := range strings.Fields(after) {
				if ids[a].end.After(t.start) {
					t.start = ids[a].end
				}
			}
		} else if t.start, ok = date(parts[0]); !ok {
			return t, "", false
		}
		parts = parts[1:]
	}
	if len(parts) != 1 || t.start.IsZero() {
		return t, "", false
	}
	end := parts[0]
	unit := map[string]time.Duration{"d": 24 * time.Hour, "w": 7 * 24 * time.Hour, "h": time.Hour}[end[max(len(end)-1, 0):]]
	if until, ok := strings.CutPrefix(end, "until "); ok {
		t.end = ids[strings.TrimSpace(until)].start
	} else if n, err := strconv.ParseFloat(strings.TrimRight(end, "dwh"), 64); err == nil && unit != 0 {
		t.end = t.start.Add(time.Duration(n * float64(unit)))
	} else if t.end, ok = date(end); !ok {
		return t, "", false
	}
	return t, id, !t.end.IsZero() && !t.end.Before(t.start)
}

func date(s string) (time.Time, bool) {
	t, err := time.Parse(time.DateOnly, s)
	return t, err == nil
}
