package mermaid

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

var (
	classOpen    = regexp.MustCompile(`^class\s+(\w+)(~[^~]+~)?(?:\["([^"]*)"\])?\s*(?:\{\s*)?$`) // class Shape {, class Canvas
	classAnno    = regexp.MustCompile(`^<<(.+)>>\s*(\w+)$`)                                       // <<interface>> Shape
	classItem    = regexp.MustCompile(`^(\w+)\s*:\s*(.+)$`)                                       // Shape : +area() float
	classGeneric = regexp.MustCompile(`~([^~]*)~`)                                                // List~Point~ is List<Point>
	classLink    = regexp.MustCompile(`^(\w+)\s*(?:"([^"]*)"\s*)?` +
		`(<\|--|--\|>|<\|\.\.|\.\.\|>|\*--|--\*|o--|--o|<--|-->|<\.\.|\.\.>|--|\.\.)` +
		`\s*(?:"([^"]*)"\s*)?(\w+)\s*(?::\s*(.*))?$`) // A "1" *-- "many" B : label
)

// relations draw each relation as an edge to its UML head, so the arrow points at the parent or the whole:
// the edge's operator, the head's symbol, and whether the head is on the left class.
var relations = map[string]struct {
	op, head string
	left     bool
}{
	"<|--": {"-->", "△", true}, "--|>": {"-->", "△", false}, "<|..": {"-.->", "△", true}, "..|>": {"-.->", "△", false},
	"*--": {"-->", "◆", true}, "--*": {"-->", "◆", false}, "o--": {"-->", "◇", true}, "--o": {"-->", "◇", false},
	"<--": {"-->", "", true}, "-->": {"-->", "", false}, "<..": {"-.->", "", true}, "..>": {"-.->", "", false},
	"--": {"---", "", false}, "..": {"---", "", false}, // mermaid-ascii has no dotted line without a head
}

// class is a class diagram as a mermaid-ascii flowchart: a class is a box with its annotation, name and members,
// a relation an arrow to its UML head labelled with the head's symbol, the cardinalities from tail to head and its label.
func class(src string) string {
	var ids, edges []string
	names, annos, members := map[string]string{}, map[string]string{}, map[string][]string{}
	declare := func(id string) string {
		if _, ok := names[id]; !ok {
			names[id] = id
			ids = append(ids, id)
		}
		return id
	}
	dir, body := "TD", "" // body is the class whose { … } the line is in
	for _, l := range strings.Split(src, "\n") {
		l = strings.TrimSpace(l)
		if body != "" {
			switch {
			case l == "}":
				body = ""
			case strings.HasPrefix(l, "<<"):
				annos[body] = strings.Trim(l, "<>")
			case l != "":
				members[body] = append(members[body], l)
			}
			continue
		}
		open, anno, item, link := classOpen.FindStringSubmatch(l), classAnno.FindStringSubmatch(l), classItem.FindStringSubmatch(l), classLink.FindStringSubmatch(l)
		switch {
		case open != nil:
			id := declare(open[1])
			names[id] = id + open[2]
			if open[3] != "" {
				names[id] = open[3]
			}
			if strings.HasSuffix(l, "{") {
				body = id
			}
		case anno != nil:
			annos[declare(anno[2])] = anno[1]
		case link != nil:
			r, from, to, fromN, toN := relations[link[3]], declare(link[1]), declare(link[5]), link[2], link[4]
			if r.left {
				from, to, fromN, toN = to, from, toN, fromN
			}
			edge := from + " " + r.op
			if label := strings.Join(strings.Fields(fromN+" "+r.head+" "+toN+" "+link[6]), " "); label != "" {
				edge += "|" + strings.ReplaceAll(label, "|", "/") + "|"
			}
			edges = append(edges, edge+" "+to)
		case item != nil:
			members[declare(item[1])] = append(members[declare(item[1])], item[2])
		case strings.HasPrefix(l, "direction "):
			dir = strings.TrimSpace(strings.TrimPrefix(l, "direction"))
		}
	}

	var b strings.Builder
	b.WriteString("graph " + dir + "\n")
	for _, id := range ids {
		lines := []string{names[id]}
		if annos[id] != "" {
			lines = append([]string{"«" + annos[id] + "»"}, lines...)
		}
		if ms := members[id]; len(ms) > 0 {
			w := 0
			for _, l := range append(lines, ms...) {
				w = max(w, ansi.StringWidth(classGeneric.ReplaceAllString(l, "<$1>")))
			}
			lines = append(append(lines, strings.Repeat("─", w)), ms...)
		}
		b.WriteString(id + `["` + classGeneric.ReplaceAllString(strings.Join(lines, "<br>"), "<$1>") + "\"]\n")
	}
	b.WriteString(strings.Join(edges, "\n"))
	return b.String()
}
