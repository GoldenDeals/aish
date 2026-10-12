package mermaid

import (
	"cmp"
	"regexp"
	"strings"
)

var (
	stateEdge  = regexp.MustCompile(`^(\S+)\s*-->\s*([^\s:]+)\s*(?::\s*(.*))?$`)  // A --> B : label
	stateFrame = regexp.MustCompile(`^state\s+(?:"([^"]*)"\s+as\s+)?(\S+)\s*\{$`) // state Connected {
	stateAlias = regexp.MustCompile(`^state\s+"([^"]*)"\s+as\s+(\S+)$`)           // state "Long name" as S
	stateText  = regexp.MustCompile(`^([\w-]+)\s*:\s*(.+)$`)                      // S : description
)

// state is a state diagram as a mermaid-ascii flowchart: a state is a box, a composite state a frame, and [*] is ●
// where its frame starts and ◉ where it ends. Nodes are declared before the edges, so each lands in its frame.
// shortcut: transitions to and from a composite state attach to its ●, as mermaid-ascii doesn't route to frames.
func state(src string) string {
	type frame struct {
		title string
		ids   []string // its states, in the order they first appear
		inner []string // its composite states
	}
	frames := map[string]*frame{"": {}}
	for _, l := range strings.Split(src, "\n") {
		if m := stateFrame.FindStringSubmatch(strings.TrimSpace(l)); m != nil {
			frames[m[2]] = &frame{title: m[2]}
			if m[1] != "" {
				frames[m[2]].title = m[1]
			}
		}
	}
	names, descs, seen := map[string]string{}, map[string][]string{}, map[string]bool{}
	open := []string{""} // the frames around the line, innermost last
	declare := func(id string) string {
		if !seen[id] {
			seen[id] = true
			f := frames[open[len(open)-1]]
			f.ids = append(f.ids, id)
		}
		return id
	}
	node := func(id string, to bool) string {
		cur := open[len(open)-1]
		switch _, composite := frames[id]; {
		case id == "[*]" && to:
			return declare("_end_" + cur)
		case id == "[*]":
			return declare("_start_" + cur)
		case composite:
			return "_start_" + id // declared when its frame opens
		}
		return declare(id)
	}

	dir, edges := "TD", []string{}
	for _, l := range strings.Split(src, "\n") {
		l = strings.TrimSpace(l)
		if m := stateFrame.FindStringSubmatch(l); m != nil {
			frames[open[len(open)-1]].inner = append(frames[open[len(open)-1]].inner, m[2])
			open = append(open, m[2])
			declare("_start_" + m[2])
			continue
		}
		edge, alias, text := stateEdge.FindStringSubmatch(l), stateAlias.FindStringSubmatch(l), stateText.FindStringSubmatch(l)
		switch {
		case l == "}" && len(open) > 1:
			open = open[:len(open)-1]
		case edge != nil:
			from, to := node(edge[1], false), node(edge[2], true)
			if label := strings.ReplaceAll(edge[3], "|", "/"); label != "" {
				from += " -->|" + label + "|"
			} else {
				from += " -->"
			}
			edges = append(edges, from+" "+to)
		case alias != nil:
			names[declare(alias[2])] = alias[1]
		case text != nil:
			id := declare(text[1])
			descs[id] = append(descs[id], text[2])
		case strings.HasPrefix(l, "direction ") && len(open) == 1:
			dir = strings.TrimSpace(strings.TrimPrefix(l, "direction"))
		}
	}

	var b strings.Builder
	b.WriteString("graph " + dir + "\n")
	var emit func(name string)
	emit = func(name string) {
		for _, id := range frames[name].ids {
			label := cmp.Or(names[id], id)
			switch {
			case strings.HasPrefix(id, "_start_"):
				label = "●"
			case strings.HasPrefix(id, "_end_"):
				label = "◉"
			}
			for _, d := range descs[id] {
				label += "<br>" + d
			}
			b.WriteString(id + `["` + label + "\"]\n")
		}
		for _, in := range frames[name].inner {
			b.WriteString("subgraph " + frames[in].title + "\n")
			emit(in)
			b.WriteString("end\n")
		}
	}
	emit("")
	b.WriteString(strings.Join(edges, "\n"))
	return b.String()
}
