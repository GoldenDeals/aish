package policy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Cache keeps the compiled policies of a directory for an agent that
// outlives one request, and compiles them again only when a file there
// or the rules change: parsing and validating the policies takes longer
// than a request should.
type Cache struct {
	mu    sync.Mutex
	dir   string
	stamp string // the files, their sizes and mtimes, and the rules
	eng   *Engine
}

// Engine returns the policies of dir and the rules, compiled now or
// earlier. The rules come from a config read anew for every request, so
// an edit of config.toml is in force from the next one.
func (c *Cache) Engine(ctx context.Context, dir string, rules Rules) (*Engine, error) {
	stamp := stamp(dir) + fmt.Sprintf("%q", rules)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.eng != nil && c.dir == dir && c.stamp == stamp {
		return c.eng, nil
	}
	eng, err := Load(ctx, dir, rules)
	if err != nil {
		return nil, err
	}
	c.dir, c.stamp, c.eng = dir, stamp, eng
	return eng, nil
}

func stamp(dir string) string {
	// A Rego file is a load error, so its arrival must be noticed too.
	var files []string
	for _, d := range filepath.SplitList(dir) {
		for _, pat := range []string{"*.cedar", "*.rego"} {
			fs, _ := filepath.Glob(filepath.Join(d, pat))
			files = append(files, fs...)
		}
	}
	var b strings.Builder
	for _, f := range files {
		if st, err := os.Stat(f); err == nil {
			fmt.Fprintf(&b, "%s\x00%d\x00%d\x00", f, st.Size(), st.ModTime().UnixNano())
		}
	}
	return b.String()
}
