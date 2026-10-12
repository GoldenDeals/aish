// Package patterns finds the files a context module reads, by the glob patterns from its config.
package patterns

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Files globs c["patterns"], a list of strings, or def without it, in order and without repeats.
// Patterns are relative to the working directory, "~/" is the home directory.
// shortcut: filepath.Glob has no "**", switch to fs.WalkDir if nested layouts are needed
func Files(c map[string]any, def []string) ([]string, error) {
	list := def
	if v, ok := c["patterns"]; ok {
		items, ok := v.([]any)
		if !ok {
			return nil, errors.New("patterns: want a list of globs")
		}
		list = nil
		for _, it := range items {
			s, ok := it.(string)
			if !ok {
				return nil, fmt.Errorf("patterns: %v is not a string", it)
			}
			list = append(list, s)
		}
	}
	home, homeErr := os.UserHomeDir()
	var files []string
	seen := map[string]bool{}
	for _, p := range list {
		if rest, ok := strings.CutPrefix(p, "~/"); ok {
			if homeErr != nil {
				continue // no home dir, nothing to find there
			}
			p = filepath.Join(home, rest)
		}
		matches, err := filepath.Glob(p)
		if err != nil {
			return nil, fmt.Errorf("pattern %q: %w", p, err)
		}
		for _, f := range matches {
			if !seen[f] {
				seen[f] = true
				files = append(files, f)
			}
		}
	}
	return files, nil
}
