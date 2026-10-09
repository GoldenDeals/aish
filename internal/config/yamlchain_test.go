package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// ruleSets are the lists that add up in the tests, as in the user's config.
var ruleSets = []string{"policy.deny", "policy.ask", "mask", "journal_ignore", "state_ignore"}

// files writes each of contents, a path relative to dir → what is in it.
func files(t *testing.T, dir string, contents map[string]string) {
	t.Helper()
	for name, data := range contents {
		put(t, filepath.Join(dir, name), data, 0o644)
	}
}

// loadYAML reads the chain of root, merges it and decodes it into dst.
func loadYAML(root, inside string, dst any, o decodeOpts) (*tree, error) {
	c, err := readChain(root, inside)
	if err != nil {
		return nil, err
	}
	tr, err := mergeChain(c, ruleSets)
	if err != nil {
		return nil, err
	}
	return tr, decodeTree(tr, dst, o)
}

// paths are the files of c, relative to dir.
func paths(t *testing.T, c chain, dir string) []string {
	t.Helper()
	var ps []string
	for _, f := range c.files {
		rel, err := filepath.Rel(dir, f.path)
		if err != nil {
			t.Fatal(err)
		}
		ps = append(ps, rel)
	}
	return ps
}

func wantErr(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || err.Error() != want {
		t.Fatalf("error %v, want %s", err, want)
	}
}

// A path is taken from the directory of the file that includes it, not
// the root's; ~/ is the home directory.
func TestChainRelative(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	t.Setenv("HOME", home)
	files(t, dir, map[string]string{
		"config.yaml":     "include: [sub/a.yaml, ~/h.yaml]\n",
		"sub/a.yaml":      "include: b.yaml\n",
		"sub/b.yaml":      "model: x\n",
		"b.yaml":          "model: decoy\n",
		"home/h.yaml":     "max_steps: 3\n",
		"home/other.yaml": "max_steps: 4\n",
	})
	c, err := readChain(filepath.Join(dir, "config.yaml"), "")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := paths(t, c, dir), []string{"config.yaml", "sub/a.yaml", "sub/b.yaml", "home/h.yaml"}; !slices.Equal(got, want) {
		t.Fatalf("files %q, want %q", got, want)
	}
	if string(c.files[2].data) != "model: x\n" {
		t.Fatalf("sub/b.yaml read as %q", c.files[2].data)
	}
}

// A glob takes the regular files it matches, sorted, a hidden one only for
// a pattern starting with a dot; one matching nothing is no error.
func TestChainGlob(t *testing.T) {
	dir := t.TempDir()
	files(t, dir, map[string]string{
		"config.yaml":            "include:\n  - profiles/*.yaml\n  - secrets*.yaml\n  - hidden/.*.yaml\n",
		"profiles/b.yaml":        "profiles: {b: {}}\n",
		"profiles/a.yaml":        "profiles: {a: {}}\n",
		"profiles/.swap.yaml":    "profiles: {swap: {}}\n",
		"profiles/dir.yaml/x":    "",
		"hidden/.one.yaml":       "max_steps: 1\n",
		"hidden/two.yaml":        "max_steps: 2\n",
		"[x]/config.yaml":        "include: '*.yaml'\n",
		"[x]/x.yaml":             "model: x\n",
		"other/profiles/c.yaml":  "",
		"profiles/notyaml.yml":   "",
		"profiles/sub/deep.yaml": "",
	})
	mkfifo(t, filepath.Join(dir, "profiles", "fifo.yaml"))
	c := quick(t, filepath.Join(dir, "profiles", "fifo.yaml"), func() chain {
		c, err := readChain(filepath.Join(dir, "config.yaml"), "")
		if err != nil {
			t.Error(err)
		}
		return c
	})
	if got, want := paths(t, c, dir), []string{"config.yaml", "profiles/a.yaml", "profiles/b.yaml", "hidden/.one.yaml"}; !slices.Equal(got, want) {
		t.Fatalf("files %q, want %q", got, want)
	}
	if len(c.globs) != 3 || c.globs[1].pattern != filepath.Join(dir, "secrets*.yaml") || c.globs[1].matches != nil {
		t.Fatalf("globs %+v", c.globs)
	}
	if got := c.globs[0].matches; !slices.Equal(got, []string{filepath.Join(dir, "profiles/a.yaml"), filepath.Join(dir, "profiles/b.yaml")}) {
		t.Fatalf("matches %q", got)
	}

	// A [ of the directory is a name, not a class.
	c, err := readChain(filepath.Join(dir, "[x]", "config.yaml"), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := paths(t, c, dir); !slices.Equal(got, []string{"[x]/config.yaml", "[x]/x.yaml"}) {
		t.Fatalf("files %q", got)
	}
}

// A path that is not there is an error at its include, with the line; the
// root not there is no config, which the caller tells by fs.ErrNotExist.
func TestChainMissing(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "config.yaml")
	files(t, dir, map[string]string{"config.yaml": "model: x\ninclude:\n  - a.yaml\n  - mcp.yml\n", "a.yaml": ""})
	_, err := readChain(root, "")
	wantErr(t, err, root+`:4: include "mcp.yml": no such file`)
	if errors.Is(err, fs.ErrNotExist) {
		t.Fatal("a missing include reads as a missing config")
	}
	if _, err := readChain(filepath.Join(dir, "none.yaml"), ""); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing root: %v", err)
	}
	files(t, dir, map[string]string{"config.yaml": "include: ''\n"})
	_, err = readChain(root, "")
	wantErr(t, err, root+":1: include: an empty path")
	files(t, dir, map[string]string{"config.yaml": "include:\n  - {a: b}\n"})
	_, err = readChain(root, "")
	wantErr(t, err, root+":2: include: want a path or a list of paths")
	files(t, dir, map[string]string{"config.yaml": "include: '**/*.yaml'\n"})
	_, err = readChain(root, "")
	wantErr(t, err, root+`:1: include "**/*.yaml": ** is not supported, a pattern matches in one directory`)
	files(t, dir, map[string]string{"config.yaml": "include: sub\n", "sub/x": ""})
	_, err = readChain(root, "")
	wantErr(t, err, root+`:1: include "sub": not a regular file`)
}

// A file is read once: a cycle ends, and a file two others include is
// read where the first does.
func TestChainOnce(t *testing.T) {
	dir := t.TempDir()
	files(t, dir, map[string]string{
		"config.yaml": "include: [b.yaml, c.yaml, config.yaml]\n",
		"b.yaml":      "include: [d.yaml, ./config.yaml]\n",
		"c.yaml":      "include: [d.yaml, link.yaml]\n",
		"d.yaml":      "include: b.yaml\n",
	})
	if err := os.Symlink("d.yaml", filepath.Join(dir, "link.yaml")); err != nil {
		t.Fatal(err)
	}
	c, err := readChain(filepath.Join(dir, "config.yaml"), "")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := paths(t, c, dir), []string{"config.yaml", "b.yaml", "d.yaml", "c.yaml"}; !slices.Equal(got, want) {
		t.Fatalf("files %q, want %q", got, want)
	}
}

// A FIFO, included or the root, is not waited on.
func TestChainFIFO(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "config.yaml")
	fifo := filepath.Join(dir, "fifo.yaml")
	mkfifo(t, fifo)
	files(t, dir, map[string]string{"config.yaml": "include: fifo.yaml\n"})
	err := quick(t, fifo, func() error { _, err := readChain(root, ""); return err })
	wantErr(t, err, root+`:1: include "fifo.yaml": not a regular file`)
	err = quick(t, fifo, func() error { _, err := readChain(fifo, ""); return err })
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("FIFO as the root: %v", err)
	}
}

// More than maxChain files is an error, at the include that brings one.
func TestChainLimit(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "config.yaml")
	files(t, dir, map[string]string{"config.yaml": "include: [many/*.yaml]\n"})
	for i := range maxChain - 1 {
		files(t, dir, map[string]string{fmt.Sprintf("many/%02d.yaml", i): ""})
	}
	c, err := readChain(root, "")
	if err != nil || len(c.files) != maxChain {
		t.Fatalf("%d files: %v", len(c.files), err)
	}
	files(t, dir, map[string]string{"many/99.yaml": ""})
	_, err = readChain(root, "")
	wantErr(t, err, root+`:1: include "many/*.yaml": more than 64 files in the chain`)
}

// A project's chain stays in its repository: a path up, an absolute one or
// a link out is an error, a link within is not.
func TestChainInside(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	root := filepath.Join(repo, ".aish.yaml")
	files(t, dir, map[string]string{
		"secrets.yaml":         "api_key: x\n",
		"repo/.aish/in.yaml":   "max_steps: 3\n",
		"repo/sub/.aish.yaml":  "include: ../.aish/in.yaml\n",
		"repo/.aish/back.yaml": "include: ../../repo/.aish/in.yaml\n",
	})
	if err := os.Symlink("../../secrets.yaml", filepath.Join(repo, ".aish", "out.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("in.yaml", filepath.Join(repo, ".aish", "link.yaml")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ include, line string }{
		{"../secrets.yaml", `include "../secrets.yaml"`},
		{filepath.Join(dir, "secrets.yaml"), fmt.Sprintf("include %q", filepath.Join(dir, "secrets.yaml"))},
		{".aish/out.yaml", `include ".aish/out.yaml"`},
		{".aish/*.yaml", `include ".aish/*.yaml"`},
	} {
		files(t, dir, map[string]string{"repo/.aish.yaml": "include: " + tc.include + "\n"})
		_, err := readChain(root, repo)
		wantErr(t, err, root+":1: "+tc.line+": outside the repository "+repo)
	}
	for _, inc := range []string{".aish/link.yaml", ".aish/back.yaml"} {
		files(t, dir, map[string]string{"repo/.aish.yaml": "include: " + inc + "\n"})
		c, err := readChain(root, repo)
		if err != nil || len(c.files) < 2 {
			t.Fatalf("%s: %v", inc, err)
		}
	}
	// A file of a monorepo includes the one at its root.
	c, err := readChain(filepath.Join(repo, "sub", ".aish.yaml"), repo)
	if err != nil || len(c.files) != 2 {
		t.Fatalf("sub/.aish.yaml: %d files, %v", len(c.files), err)
	}
	// Without a bound the user's chain goes where it likes.
	files(t, dir, map[string]string{"repo/.aish.yaml": "include: ../secrets.yaml\n"})
	if _, err := readChain(root, ""); err != nil {
		t.Fatal(err)
	}
}

// A file of comments is no keys; anchors, a second document and a key set
// twice in a file are errors with their lines; a value YAML takes for
// syntax gets a hint.
func TestChainParse(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "config.yaml")
	for _, data := range []string{"", "# only a comment\n", "---\n", "~\n"} {
		files(t, dir, map[string]string{"config.yaml": data})
		c, err := readChain(root, "")
		if err != nil || len(c.files) != 1 || c.files[0].tree != nil {
			t.Fatalf("%q: %+v, %v", data, c.files, err)
		}
	}
	anchors := "anchors are not supported: profiles inherit the top level, files include each other"
	for _, tc := range []struct{ data, want string }{
		{"model: x\nprofiles:\n  work: &w\n    model: y\n", ":3: " + anchors},
		{"a: &x 1\nb: *x\n", ":1: " + anchors},
		{"profiles:\n  work:\n    <<: {model: y}\n", ":3: " + anchors},
		{"model: x\n---\nmodel: y\n", ":2: a second document (---): a config file holds one"},
		{"- a\n", ":1: want keys at the top level, not a list or a value"},
		{"model: x\nroute:\n  suffix: a\n  suffix: b\n", ":4: route.suffix is also set in " + root + ":3"},
		{"journal_ignore:\n  - *secret*\n", `:2: did not find expected alphabetic or numeric character (quote a value that starts with * or &: "*secret*")`},
		{"model: x\nroute:\n  suffix: ?\n", `:3: mapping keys are not allowed in this context (quote a value that starts with ?: "?")`},
		{"system_prompt: Note: this\n", `:1: mapping values are not allowed in this context (quote a value with ": " in it)`},
		{"route:\n\tsuffix: x\n", ":2: found character that cannot start any token (indent with spaces, not tabs; quote a value that starts with @, ` or %)"},
		{"model: x\nmask: [x, y\n", ":2: did not find expected ',' or ']'"},
		{"model: x\nroute:\n  suffix: x\n capital: true\n", ":4: did not find expected key"},
		{"model: x\napi_key: *sk-ant-secret\n", ":2: quote a value that starts with *: " + anchors},
		{"include: a.yaml\nx:\n  include: b.yaml\n  include: c.yaml\n", ":4: x.include is also set in " + root + ":3"},
	} {
		files(t, dir, map[string]string{"config.yaml": tc.data, "a.yaml": ""})
		_, err := readChain(root, "")
		wantErr(t, err, root+tc.want)
	}
}
