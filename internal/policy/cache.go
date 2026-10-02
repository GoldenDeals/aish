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
// changes: preparing a Rego query takes longer than a request should.
type Cache struct {
	mu    sync.Mutex
	dir   string
	stamp string // the files, their sizes and mtimes
	eng   *Engine
}

// Engine returns the policies of dir, compiled now or earlier.
func (c *Cache) Engine(ctx context.Context, dir string) (*Engine, error) {
	stamp := stamp(dir)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.eng != nil && c.dir == dir && c.stamp == stamp {
		return c.eng, nil
	}
	eng, err := Load(ctx, dir)
	if err != nil {
		return nil, err
	}
	c.dir, c.stamp, c.eng = dir, stamp, eng
	return eng, nil
}

func stamp(dir string) string {
	var files []string
	for _, d := range filepath.SplitList(dir) {
		fs, _ := filepath.Glob(filepath.Join(d, "*.rego"))
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
