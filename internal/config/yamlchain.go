package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// maxChain bounds the files of a chain: a glob over a big directory is a
// mistake, not a config.
const maxChain = 64

// chain is a config file and the files it includes, and those they do:
// all of them equals, merged by mergeChain.
type chain struct {
	// files are in the order read, depth first: a file, then each of its
	// includes with what that one includes, in the order written.
	files []chainFile
	// globs are the patterns of include as read, so that a file new under
	// one, or gone, tells the chain from the one read.
	globs []chainGlob
}

type chainFile struct {
	// path is the file as included: absolute, its links kept.
	path string
	data []byte
	// tree is the map at the top of the file without include, nil for a
	// file of no keys.
	tree *tnode
}

type chainGlob struct {
	// pattern is absolute, as matched.
	pattern string
	// matches are the regular files it gave, sorted, those read before
	// too.
	matches []string
}

// readChain reads the config file root and those it includes. inside,
// unless "", bounds the files included: a project's chain stays in its
// repository, lest it read a file of the user's (a link to ~/.ssh/config, a
// secrets.yaml) into a config that a request or an error shows. root itself
// is not bound: it is the file found in the directory, which findProject
// takes as a link too.
//
// include is a path or a list of them, on the top level of a file: relative
// to the directory of the file, absolute or ~/; variables are not expanded,
// the proxy's are not the shell's. A path with *, ? or [ is a glob, as
// filepath.Match takes it: its matches go in sorted, a file starting with a
// dot only where the pattern's element does, and directories and other
// files that are not regular are left out. A glob matching nothing is no
// error, so that a file may be there or not (secrets*.yaml); a path that is
// not there is one. A file is read once, by its path with links resolved:
// a second include of it, a cycle too, is skipped.
//
// A root that is not there is an error wrapping fs.ErrNotExist, a file
// included that is not there is not: one is no config, the other a broken
// one. With an error the chain holds the files read before it.
func readChain(root, inside string) (chain, error) {
	r := chainReader{seen: map[string]bool{}}
	if inside != "" {
		abs, err := filepath.Abs(inside)
		if err != nil {
			return chain{}, err
		}
		real, err := filepath.EvalSymlinks(abs)
		if err != nil {
			// Not wrapped: a repository gone is not a config missing.
			return chain{}, fmt.Errorf("%s: %v", abs, unwrapPath(err))
		}
		r.inside, r.insideReal = abs, real
	}
	path, err := filepath.Abs(root)
	if err != nil {
		return chain{}, err
	}
	data, err := readRegular(path)
	if err != nil {
		return chain{}, err
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return chain{}, err
	}
	r.seen[real] = true
	err = r.read(path, data)
	return r.c, err
}

type chainReader struct {
	c chain
	// inside is the bound as given, for the error; insideReal with its links
	// resolved, as the files are checked.
	inside, insideReal string
	seen               map[string]bool
}

// include is a path an include of a file names, and where.
type include struct {
	at   place
	name string
}

// read takes the file at path and then what it includes.
func (r *chainReader) read(path string, data []byte) error {
	tree, incs, err := parseFile(path, data)
	if err != nil {
		return err
	}
	r.c.files = append(r.c.files, chainFile{path: path, data: data, tree: tree})
	for _, inc := range incs {
		if err := r.include(path, inc); err != nil {
			return err
		}
	}
	return nil
}

func (r *chainReader) include(from string, inc include) error {
	fail := func(format string, a ...any) error {
		return fmt.Errorf("%s: include %q: %s", inc.at, inc.name, fmt.Sprintf(format, a...))
	}
	name := inc.name
	if name == "" {
		return fmt.Errorf("%s: include: an empty path", inc.at)
	}
	if !strings.ContainsAny(name, "*?[") {
		return r.add(inc, fail, resolve(from, name, false))
	}
	if strings.Contains(name, "**") {
		return fail("** is not supported, a pattern matches in one directory")
	}
	pattern := resolve(from, name, true)
	all, err := filepath.Glob(pattern)
	if err != nil {
		return fail("%v", err)
	}
	var matches []string
	for _, m := range all {
		if st, err := os.Stat(m); err == nil && st.Mode().IsRegular() && !hidden(pattern, m) {
			matches = append(matches, m)
		}
	}
	slices.Sort(matches)
	r.c.globs = append(r.c.globs, chainGlob{pattern: pattern, matches: matches})
	for _, m := range matches {
		if err := r.add(inc, fail, m); err != nil {
			return err
		}
	}
	return nil
}

// resolve is the path name names in the file from: relative to its
// directory, unless absolute or ~/. In a pattern the directory is quoted:
// a [ in it is a name, not a class.
func resolve(from, name string, pattern bool) string {
	base := filepath.Dir(from)
	if rest, ok := strings.CutPrefix(name, "~/"); ok {
		base, _ = os.UserHomeDir()
		name = rest
	} else if filepath.IsAbs(name) {
		return filepath.Clean(name)
	}
	if pattern {
		base = globQuote.ReplaceAllString(base, `\$0`)
	}
	return filepath.Join(base, name)
}

var globQuote = regexp.MustCompile(`[*?\[\\]`)

// hidden tells a match of pattern that a shell would not give: a name
// starting with a dot where the pattern's element does not.
func hidden(pattern, match string) bool {
	ps := strings.Split(pattern, string(filepath.Separator))
	ms := strings.Split(match, string(filepath.Separator))
	if len(ps) != len(ms) {
		return false
	}
	for i, p := range ps {
		if strings.HasPrefix(ms[i], ".") && !strings.HasPrefix(p, ".") {
			return true
		}
	}
	return false
}

// add reads the file path that inc gave, unless it was read already.
func (r *chainReader) add(inc include, fail func(string, ...any) error, path string) error {
	real, err := filepath.EvalSymlinks(path)
	if errors.Is(err, fs.ErrNotExist) {
		return fail("no such file")
	}
	if err != nil {
		return fail("%v", unwrapPath(err))
	}
	if r.inside != "" && !within(r.insideReal, real) {
		return fail("outside the repository %s", r.inside)
	}
	if r.seen[real] {
		return nil
	}
	if len(r.c.files) >= maxChain {
		return fail("more than %d files in the chain", maxChain)
	}
	if st, err := os.Stat(path); err != nil {
		return fail("%v", unwrapPath(err))
	} else if !st.Mode().IsRegular() {
		return fail("not a regular file")
	}
	data, err := readRegular(path)
	if err != nil {
		return fail("%v", unwrapPath(err))
	}
	r.seen[real] = true
	return r.read(path, data)
}

// unwrapPath is err without the path an *fs.PathError repeats: the error
// names the include already.
func unwrapPath(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// parseFile is the tree of a config file and what it includes. A file of
// no keys, comments only too, is an empty config; a second document is an
// error rather than a config read in half.
func parseFile(path string, data []byte) (*tnode, []include, error) {
	d := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := d.Decode(&doc); err == io.EOF {
		return nil, nil, nil
	} else if err != nil {
		return nil, nil, syntaxError(path, data, err)
	}
	var next yaml.Node
	if err := d.Decode(&next); err == nil {
		return nil, nil, fmt.Errorf("%s: a second document (---): a config file holds one", place{path, next.Line})
	} else if err != io.EOF {
		return nil, nil, syntaxError(path, data, err)
	}
	if len(doc.Content) == 0 {
		return nil, nil, nil
	}
	top := doc.Content[0]
	if top.Kind == yaml.ScalarNode && top.ShortTag() == "!!null" && top.Anchor == "" {
		return nil, nil, nil
	}
	if top.Kind != yaml.MappingNode {
		return nil, nil, fmt.Errorf("%s: want keys at the top level, not a list or a value", place{path, top.Line})
	}
	tree, err := convert(path, "", place{path, top.Line}, top)
	if err != nil {
		return nil, nil, err
	}
	n, ok := tree.sub["include"]
	if !ok {
		return tree, nil, nil
	}
	tree.remove("include")
	var incs []include
	items := []*tnode{n}
	if n.kind == yaml.SequenceNode {
		items = n.items
	}
	for _, it := range items {
		if it.kind != yaml.ScalarNode || it.isNull() {
			return nil, nil, fmt.Errorf("%s: include: want a path or a list of paths", it.at)
		}
		incs = append(incs, include{at: it.at, name: it.value.Value})
	}
	return tree, incs, nil
}

var (
	yamlLine  = regexp.MustCompile(`^yaml: (?:line (\d+): )?`)
	yamlAlias = regexp.MustCompile(`anchor '(.*)'`)
)

// yamlParsing are the errors of yaml's parser, as opposed to its scanner:
// the line it gives them is counted from 0.
var yamlParsing = []string{
	"did not find expected <",
	"did not find expected node content",
	"did not find expected '-' indicator",
	"did not find expected key",
	"did not find expected ',' or ",
	"found undefined tag handle",
	"found duplicate %",
	"found incompatible YAML document",
}

// yamlHints say what a value that YAML reads as syntax wants: most often
// quotes, which the patterns of the rules need.
var yamlHints = []struct{ err, hint string }{
	{"did not find expected alphabetic or numeric character", `quote a value that starts with * or &: "*secret*"`},
	{"mapping keys are not allowed in this context", `quote a value that starts with ?: "?"`},
	{"mapping values are not allowed in this context", `quote a value with ": " in it`},
	{"found character that cannot start any token", "indent with spaces, not tabs; quote a value that starts with @, ` or %"},
}

// syntaxError is err of yaml as "file:line: what", with a hint. Its text
// is yaml's own but for an alias: its name is a part of the value, which
// may be a key, and a value starting with * is a pattern more often than
// an alias.
func syntaxError(path string, data []byte, err error) error {
	msg := err.Error()
	line := 1 // yaml leaves out line 0, the first
	if m := yamlLine.FindStringSubmatch(msg); m != nil {
		msg = msg[len(m[0]):]
		if m[1] != "" {
			line, _ = strconv.Atoi(m[1])
			if slices.ContainsFunc(yamlParsing, func(p string) bool { return strings.HasPrefix(msg, p) }) {
				line++
			}
		}
	}
	if m := yamlAlias.FindStringSubmatch(msg); m != nil {
		for i, l := range bytes.Split(data, []byte("\n")) {
			if bytes.Contains(l, []byte("*"+m[1])) {
				return fmt.Errorf("%s: quote a value that starts with *: %w", place{path, i + 1}, errAnchors)
			}
		}
		return fmt.Errorf("%s: quote a value that starts with *: %w", path, errAnchors)
	}
	for _, h := range yamlHints {
		if strings.HasPrefix(msg, h.err) {
			msg += " (" + h.hint + ")"
			break
		}
	}
	return fmt.Errorf("%s: %s", place{path, line}, msg)
}
