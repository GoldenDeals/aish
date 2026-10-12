package mermaid

import (
	"regexp"
	"strings"
)

// mindShape is a node written in a shape: id[text], id((text)), id))text((, id{{text}} and the rest.
var mindShape = regexp.MustCompile(`^\S*?(?:\(\(|\)\)|\{\{|\[|\(|\))(.*?)(?:\)\)|\(\(|\}\}|\]|\)|\()$`)

// mindmap is a mindmap drawn as a tree: the root, then each node under its parent, nested by indentation.
func mindmap(src string) string {
	type node struct {
		depth int
		text  string
	}
	var nodes []node
	var indents []int                                                   // of the node's ancestors and the node itself
	for _, l := range strings.Split(strings.TrimSpace(src), "\n")[1:] { // past the header
		text, _, _ := strings.Cut(strings.TrimSpace(l), ":::") // :::class
		if text = strings.TrimSpace(text); text == "" || strings.HasPrefix(text, "::icon(") || strings.HasPrefix(text, "%%") {
			continue
		}
		if m := mindShape.FindStringSubmatch(text); m != nil {
			text = m[1]
		}
		in := len(l) - len(strings.TrimLeft(l, " \t"))
		for len(indents) > 0 && indents[len(indents)-1] >= in {
			indents = indents[:len(indents)-1]
		}
		indents = append(indents, in)
		nodes = append(nodes, node{len(indents) - 1, text})
	}

	var b strings.Builder
	var more []bool // per depth above the node's: whether that ancestor has siblings below, so its line goes on
	for i, n := range nodes {
		if n.depth == 0 {
			b.WriteString(n.text + "\n")
			continue
		}
		next := false
		for _, after := range nodes[i+1:] {
			if after.depth <= n.depth {
				next = after.depth == n.depth
				break
			}
		}
		more = append(more[:n.depth-1], next)
		for _, m := range more[:n.depth-1] {
			if m {
				b.WriteString("│   ")
			} else {
				b.WriteString("    ")
			}
		}
		if next {
			b.WriteString("├── ")
		} else {
			b.WriteString("└── ")
		}
		b.WriteString(n.text + "\n")
	}
	return b.String()
}
