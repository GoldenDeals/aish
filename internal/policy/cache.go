package policy

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// Cache keeps the compiled policies for an agent that outlives one
// request: parsing and validating them takes longer than a request
// should. The policies of a list of directories with the rules are
// compiled the first time a request needs them and kept, whatever happens
// to their files, until Reset (`aish apply-config`): an edit is in force
// once the user applies it, with the rest of the config. Several are kept, as requests come from projects
// with directories of their own. Changed tells that the files differ.
type Cache struct {
	mu   sync.Mutex
	engs map[string]compiled // by dir and rules
}

type compiled struct {
	eng    *Engine
	dirs   []string // the directories of the list
	stamps []string // of each, when compiled
}

// Engine returns the policies of dir and the rules, compiled now or
// earlier. An error is not kept: no policies are in force to keep, and
// the request that needs them is refused; the next one compiles anew.
func (c *Cache) Engine(ctx context.Context, dir string, rules Rules) (*Engine, error) {
	// %#v quotes the strings and sorts the hints, and takes Builtin too.
	key := fmt.Sprintf("%q %#v", dir, rules)
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.engs[key]; ok {
		return e.eng, nil
	}
	// Before Load: a file changed meanwhile is told of rather than missed.
	e := compiled{dirs: filepath.SplitList(dir)}
	for _, d := range e.dirs {
		e.stamps = append(e.stamps, stamp(d))
	}
	eng, err := Load(ctx, dir, rules)
	if err != nil {
		return nil, err
	}
	e.eng = eng
	if c.engs == nil {
		c.engs = map[string]compiled{}
	}
	c.engs[key] = e
	return eng, nil
}

// Reset drops the policies c keeps for those fresh compiled, which it
// leaves empty: `aish apply-config` compiles the policies of the config
// read anew before it puts that in force, and what it checked is what
// runs.
func (c *Cache) Reset(fresh *Cache) {
	fresh.mu.Lock()
	engs := fresh.engs
	fresh.engs = nil
	fresh.mu.Unlock()
	c.mu.Lock()
	c.engs = engs
	c.mu.Unlock()
}

// Changed names the directories of the policies kept whose files differ
// now from those compiled; keys tell how each is now, so that the proxy
// says once that an edit waits to be applied, and again for the next one.
func (c *Cache) Changed() (dirs, keys []string) {
	// A directory may be in several lists, compiled at different times.
	was := map[string][]string{}
	c.mu.Lock()
	for _, e := range c.engs {
		for i, d := range e.dirs {
			was[d] = append(was[d], e.stamps[i])
		}
	}
	c.mu.Unlock()
	for _, d := range slices.Sorted(maps.Keys(was)) {
		now := stamp(d)
		if slices.ContainsFunc(was[d], func(s string) bool { return s != now }) {
			dirs = append(dirs, d)
			keys = append(keys, d+"\x00"+now)
		}
	}
	return dirs, keys
}

// stamp is the files of the policies in dir, their sizes and mtimes.
func stamp(dir string) string {
	// A Rego file is a load error, so its arrival must be noticed too.
	var files []string
	for _, pat := range []string{"*.cedar", "*.rego"} {
		fs, _ := filepath.Glob(filepath.Join(dir, pat))
		files = append(files, fs...)
	}
	var b strings.Builder
	for _, f := range files {
		if st, err := os.Stat(f); err == nil {
			fmt.Fprintf(&b, "%s\x00%d\x00%d\x00", f, st.Size(), st.ModTime().UnixNano())
		}
	}
	return b.String()
}
