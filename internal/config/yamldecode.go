package config

import (
	"fmt"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// keyTag is the struct tag naming the key of a field: the TOML one still,
// the keys being the same in both formats.
const keyTag = "toml"

// decodeOpts is what decodeTree is told besides the tags of the struct.
type decodeOpts struct {
	// pathLists are the keys, their paths joined by dots, whose value is a
	// path or a list of them, decoded into a string in the form of PATH,
	// which policy.Load, tools.Load and hooks.Find take.
	pathLists []string
	// user, unless nil, is the struct of the user's config: a key of its
	// top level that dst does not take is not allowed in a project, rather
	// than unknown.
	user any
}

// decodeTree sets the fields of the struct dst points to from the keys of
// t, by keyTag: a key no field takes is an error, so is a value of another
// type, so that a typo does not leave a default in force without a word.
// What dst holds is kept where t sets nothing: the defaults go in first. A
// map adds t's keys to those it has; a list is replaced.
//
// Types are strict: a bool is true or false, not yes or on; an integer is
// a decimal one, 1.5 is no integer; a float takes an integer too; a string
// is the text of a scalar, so that model: 1.10 is "1.10". Null is an empty
// map, and an error for anything else. Embedded structs without a tag lend
// their keys, as in encoding/json; a pointer tells a key that is set from
// one that is not, as in Profile.
//
// An error is "file:line: key: what" without the value: values hold API
// keys and the passwords of proxies.
func decodeTree(t *tree, dst any, o decodeOpts) error {
	v := reflect.ValueOf(dst)
	if v.Kind() != reflect.Pointer || v.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("decodeTree: %T is not a pointer to a struct", dst)
	}
	d := decoder{paths: map[string]bool{}}
	for _, p := range o.pathLists {
		d.paths[p] = true
	}
	if o.user != nil {
		d.user = keyFields(reflect.TypeOf(o.user))
	}
	return d.value(t.root, "", v.Elem())
}

type decoder struct {
	paths map[string]bool
	user  map[string][]int
}

var decimal = regexp.MustCompile(`^[-+]?[0-9]+$`)

func (d decoder) value(n *tnode, path string, v reflect.Value) error {
	fail := func(what string) error { return fmt.Errorf("%s: %s: %s", n.at, path, what) }
	if n.isNull() {
		switch v.Kind() {
		case reflect.Struct, reflect.Map, reflect.Pointer:
		case reflect.String:
			return fail(`use "" for an empty string`)
		case reflect.Slice:
			return fail("use [] for an empty list")
		default:
			return fail(want(v.Kind()))
		}
	}
	switch v.Kind() {
	case reflect.Pointer:
		// A new value, not the one pointed to, which may be another
		// config's.
		p := reflect.New(v.Type().Elem())
		if !v.IsNil() {
			p.Elem().Set(v.Elem())
		}
		if err := d.value(n, path, p.Elem()); err != nil {
			return err
		}
		v.Set(p)
		return nil
	case reflect.Struct:
		return d.structure(n, path, v)
	case reflect.Map:
		return d.mapping(n, path, v)
	case reflect.Slice:
		if v.Type().Elem().Kind() != reflect.String {
			return fail("cannot be decoded into " + v.Type().String())
		}
		if n.kind != yaml.SequenceNode {
			return fail("want a list")
		}
		list := reflect.MakeSlice(v.Type(), 0, len(n.items))
		for _, it := range n.items {
			if it.kind != yaml.ScalarNode || it.isNull() {
				return fmt.Errorf("%s: %s: want a list of strings", it.at, path)
			}
			list = reflect.Append(list, reflect.ValueOf(it.value.Value).Convert(v.Type().Elem()))
		}
		v.Set(list)
		return nil
	}
	if d.paths[path] && v.Kind() == reflect.String {
		return d.pathList(n, path, v)
	}
	if n.kind != yaml.ScalarNode {
		return fail(want(v.Kind()))
	}
	text, tag := n.value.Value, n.value.ShortTag()
	switch v.Kind() {
	case reflect.String:
		v.SetString(text)
	case reflect.Bool:
		switch {
		case tag == "!!bool" && strings.EqualFold(text, "true"):
			v.SetBool(true)
		case tag == "!!bool" && strings.EqualFold(text, "false"):
			v.SetBool(false)
		default:
			return fail(want(v.Kind()))
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if tag != "!!int" || !decimal.MatchString(text) {
			return fail(want(v.Kind()))
		}
		i, err := strconv.ParseInt(text, 10, 64)
		if err != nil || v.OverflowInt(i) {
			return fail("out of range")
		}
		v.SetInt(i)
	case reflect.Float32, reflect.Float64:
		if tag != "!!float" && (tag != "!!int" || !decimal.MatchString(text)) {
			return fail(want(v.Kind()))
		}
		f, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return fail(want(v.Kind()))
		}
		if v.OverflowFloat(f) {
			return fail("out of range")
		}
		v.SetFloat(f)
	default:
		return fail("cannot be decoded into " + v.Type().String())
	}
	return nil
}

// want is what a value of kind k must be, for an error.
func want(k reflect.Kind) string {
	switch k {
	case reflect.Bool:
		return "want true or false"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return "want a whole number"
	case reflect.Float32, reflect.Float64:
		return "want a number"
	case reflect.String:
		return "want a string, not a list or a map"
	}
	return "want a value"
}

func (d decoder) structure(n *tnode, path string, v reflect.Value) error {
	if n.isNull() {
		return nil
	}
	if n.kind != yaml.MappingNode {
		return fmt.Errorf("%s: %s: want a map of keys", n.at, path)
	}
	fields := keyFields(v.Type())
	for _, k := range n.keys {
		sub, kp := n.sub[k], keyPath(path, k)
		index, ok := fields[k]
		if !ok {
			if _, user := d.user[k]; user && path == "" {
				return fmt.Errorf("%s: key %q is not allowed in a project config: set it in ~/.config/aish/config.yaml", sub.at, k)
			}
			return fmt.Errorf("%s: unknown key %q", sub.at, kp)
		}
		if err := d.value(sub, kp, v.FieldByIndex(index)); err != nil {
			return err
		}
	}
	return nil
}

func (d decoder) mapping(n *tnode, path string, v reflect.Value) error {
	if v.Type().Key().Kind() != reflect.String {
		return fmt.Errorf("%s: %s: cannot be decoded into %s", n.at, path, v.Type())
	}
	if !n.isNull() && n.kind != yaml.MappingNode {
		return fmt.Errorf("%s: %s: want a map of keys", n.at, path)
	}
	// A new map, not the one held, which may be another config's.
	m := reflect.MakeMap(v.Type())
	for it := v.MapRange(); it.Next(); {
		m.SetMapIndex(it.Key(), it.Value())
	}
	for _, k := range n.keys {
		key := reflect.ValueOf(k).Convert(v.Type().Key())
		e := reflect.New(v.Type().Elem()).Elem()
		if old := m.MapIndex(key); old.IsValid() {
			e.Set(old)
		}
		if err := d.value(n.sub[k], keyPath(path, k), e); err != nil {
			return err
		}
		m.SetMapIndex(key, e)
	}
	v.Set(m)
	return nil
}

// pathList is the value of a key of pathLists: a path, or a list of them
// joined as in PATH. A path with the separator in it could not be told
// from two.
func (d decoder) pathList(n *tnode, path string, v reflect.Value) error {
	items := []*tnode{n}
	if n.kind == yaml.SequenceNode {
		items = n.items
	}
	sep := string(filepath.ListSeparator)
	var paths []string
	for _, it := range items {
		if it.kind != yaml.ScalarNode || it.isNull() {
			return fmt.Errorf("%s: %s: want a path or a list of paths", it.at, path)
		}
		p := it.value.Value
		switch {
		case strings.Contains(p, sep):
			return fmt.Errorf("%s: %s: a path with %q in it: give several as a list", it.at, path, sep)
		case p == "" && n.kind == yaml.SequenceNode:
			return fmt.Errorf("%s: %s: an empty path in the list", it.at, path)
		}
		paths = append(paths, p)
	}
	v.SetString(strings.Join(paths, sep))
	return nil
}

// keyFields are the fields of struct type t by the keys that name them:
// keyTag, or the field's name without one, "-" being none. The fields of
// an embedded struct without a tag are t's own, unless t has one of the
// name nearer.
func keyFields(t reflect.Type) map[string][]int {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	fields := map[string][]int{}
	var walk func(t reflect.Type, index []int)
	walk = func(t reflect.Type, index []int) {
		for i := range t.NumField() {
			f := t.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get(keyTag), ",")
			at := append(index[:len(index):len(index)], i)
			switch {
			case name == "-" || !f.IsExported():
			case f.Anonymous && name == "" && f.Type.Kind() == reflect.Struct:
				walk(f.Type, at)
			default:
				if name == "" {
					name = f.Name
				}
				if had, ok := fields[name]; !ok || len(had) > len(at) {
					fields[name] = at
				}
			}
		}
	}
	walk(t, nil)
	return fields
}
