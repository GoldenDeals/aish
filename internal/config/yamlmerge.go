package config

import (
	"errors"
	"fmt"
	"slices"
	"strconv"

	"go.yaml.in/yaml/v3"
)

// place is where a key or an item is written: the file and the line.
type place struct {
	file string
	line int
}

func (p place) String() string { return p.file + ":" + strconv.Itoa(p.line) }

// tnode is a node of a config tree, a file's or a chain's merged: a map of
// keys, a list or a scalar, null being a scalar too. Each keeps the place
// of the key it is the value of, or of the item it is, so that an error, a
// conflict or an unknown key names the file and the line it is about. The
// nodes of a file are not changed once read: a snapshot merges them anew
// for each config it loads.
type tnode struct {
	at    place
	kind  yaml.Kind  // MappingNode, SequenceNode or ScalarNode
	value *yaml.Node // a scalar's
	keys  []string   // a map's, in the order read
	sub   map[string]*tnode
	items []*tnode // a list's
}

func (n *tnode) isNull() bool { return n.kind == yaml.ScalarNode && n.value.ShortTag() == "!!null" }

// isMap tells a node that merges by keys: null is an empty map, so that
// `work:` with nothing under it is a profile with nothing set, as an
// empty table was in TOML.
func (n *tnode) isMap() bool { return n.kind == yaml.MappingNode || n.isNull() }

// same tells an item of a set that o repeats.
func (n *tnode) same(o *tnode) bool {
	return n.kind == yaml.ScalarNode && o.kind == yaml.ScalarNode && n.value.Value == o.value.Value
}

// errAnchors is what an anchor, an alias or a << key is: through them a key
// would be set in one place and read in another, and a value in two files
// could not be told from one written twice by an alias.
var errAnchors = errors.New("anchors are not supported: profiles inherit the top level, files include each other")

// convert is the tree of n, the value of key path, read from file; at is
// where the key is.
func convert(file, path string, at place, n *yaml.Node) (*tnode, error) {
	if n.Anchor != "" || n.Kind == yaml.AliasNode {
		return nil, fmt.Errorf("%s: %w", place{file, n.Line}, errAnchors)
	}
	t := &tnode{at: at, kind: n.Kind}
	switch n.Kind {
	case yaml.ScalarNode:
		t.value = n
	case yaml.SequenceNode:
		for _, c := range n.Content {
			item, err := convert(file, path, place{file, c.Line}, c)
			if err != nil {
				return nil, err
			}
			t.items = append(t.items, item)
		}
	case yaml.MappingNode:
		t.sub = map[string]*tnode{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			kat := place{file, k.Line}
			if k.Anchor != "" || k.Kind == yaml.AliasNode || k.ShortTag() == "!!merge" {
				return nil, fmt.Errorf("%s: %w", kat, errAnchors)
			}
			if k.Kind != yaml.ScalarNode {
				return nil, fmt.Errorf("%s: %s: a key is a word, not a list or a map", kat, keyPath(path, "?"))
			}
			kp := keyPath(path, k.Value)
			if had, ok := t.sub[k.Value]; ok {
				return nil, fmt.Errorf("%s: %s is also set in %s", kat, kp, had.at)
			}
			sub, err := convert(file, kp, kat, v)
			if err != nil {
				return nil, err
			}
			t.keys = append(t.keys, k.Value)
			t.sub[k.Value] = sub
		}
	default:
		return nil, fmt.Errorf("%s: %s: unexpected YAML node", at, path)
	}
	return t, nil
}

// remove takes key out of map t, while the file's tree is being built.
func (t *tnode) remove(key string) {
	delete(t.sub, key)
	t.keys = slices.DeleteFunc(t.keys, func(k string) bool { return k == key })
}

func keyPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// tree is the files of a chain merged into one map of keys, as the decoder
// takes it, with the place each key is set at.
type tree struct {
	root *tnode
	// nodes are the keys of the tree by their paths, the keys joined by
	// dots: their places are where the first file of the chain to have a
	// map's key sets it.
	nodes map[string]*tnode
}

// at is where key, a path of keys joined by dots, is set as "file:line",
// or "" where it is not: what a check of the merged config starts its
// error with.
func (t *tree) at(key string) string {
	if n, ok := t.nodes[key]; ok {
		return n.at.String()
	}
	return ""
}

// itemAt is where the item text of list key is written, or "": a set adds
// up the items of several files, and a bad one is in one of them.
func (t *tree) itemAt(key, text string) string {
	if n, ok := t.nodes[key]; ok {
		for _, it := range n.items {
			if it.kind == yaml.ScalarNode && it.value.Value == text {
				return it.at.String()
			}
		}
	}
	return ""
}

// merger lays the files of a chain over each other. They are equals: a
// map merges by its keys, the lists that are sets add up, and anything
// else set in two files is an error naming both, whichever is read first.
// So what a file sets cannot be quietly undone by another, and the order
// of include is only the order of the items of a set.
type merger struct {
	// sets are the paths of the lists that add up, without repeats: rules
	// such as policy.deny, which a file of its own may add to.
	sets map[string]bool
}

// mergeChain merges the files of c; sets are the paths of the lists that
// add up (policy.deny, mask…).
func mergeChain(c chain, sets []string) (*tree, error) {
	m := merger{sets: map[string]bool{}}
	for _, s := range sets {
		m.sets[s] = true
	}
	root := &tnode{kind: yaml.MappingNode, sub: map[string]*tnode{}}
	if len(c.files) > 0 {
		root.at = place{c.files[0].path, 1}
	}
	for _, f := range c.files {
		if f.tree == nil {
			continue
		}
		if err := m.merge(root, f.tree, ""); err != nil {
			return nil, err
		}
	}
	t := &tree{root: root, nodes: map[string]*tnode{}}
	t.index(root, "")
	return t, nil
}

func (t *tree) index(n *tnode, path string) {
	if n.kind != yaml.MappingNode {
		return
	}
	for _, k := range n.keys {
		kp := keyPath(path, k)
		t.nodes[kp] = n.sub[k]
		t.index(n.sub[k], kp)
	}
}

// merge lays src, of a file read after those dst is merged from, over dst,
// a node of the merged tree at path.
func (m merger) merge(dst, src *tnode, path string) error {
	switch {
	case dst.isMap() && src.isMap():
		if src.kind != yaml.MappingNode {
			return nil
		}
		if dst.kind != yaml.MappingNode {
			dst.kind, dst.value, dst.sub = yaml.MappingNode, nil, map[string]*tnode{}
		}
		for _, k := range src.keys {
			s, kp := src.sub[k], keyPath(path, k)
			if d, ok := dst.sub[k]; ok {
				if err := m.merge(d, s, kp); err != nil {
					return err
				}
				continue
			}
			d, err := m.adopt(s, kp)
			if err != nil {
				return err
			}
			dst.keys = append(dst.keys, k)
			dst.sub[k] = d
		}
		return nil
	case m.sets[path] && dst.kind == yaml.SequenceNode && src.kind == yaml.SequenceNode:
		for _, it := range src.items {
			if !slices.ContainsFunc(dst.items, it.same) {
				dst.items = append(dst.items, it)
			}
		}
		return nil
	}
	return fmt.Errorf("%s: %s is also set in %s", src.at, path, dst.at)
}

// adopt is src, at path, as a node of the merged tree of its own: the
// file's tree is not changed by what later files merge into it.
func (m merger) adopt(src *tnode, path string) (*tnode, error) {
	n := &tnode{at: src.at, kind: src.kind, value: src.value}
	switch {
	case src.kind == yaml.MappingNode:
		n.sub = map[string]*tnode{}
	case src.kind == yaml.SequenceNode && m.sets[path]:
	default:
		n.items = slices.Clone(src.items)
		return n, nil
	}
	return n, m.merge(n, src, path)
}
