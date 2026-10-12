package patterns

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestFiles(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOME", home)
	for _, f := range []string{filepath.Join(dir, "a.md"), filepath.Join(dir, "b.md"), filepath.Join(home, "h.md")} {
		if err := os.WriteFile(f, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, tc := range map[string]struct {
		cfg  map[string]any
		want []string // nil means an error
	}{
		"default": {map[string]any{}, []string{"a.md", "b.md", filepath.Join(home, "h.md")}},
		"custom":  {map[string]any{"patterns": []any{"b.md", "*.md", "none"}}, []string{"b.md", "a.md"}},
		"empty":   {map[string]any{"patterns": []any{}}, []string{}},
		"string":  {map[string]any{"patterns": "*.md"}, nil},
		"number":  {map[string]any{"patterns": []any{1}}, nil},
		"bad":     {map[string]any{"patterns": []any{"["}}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := Files(tc.cfg, []string{"*.md", "~/*.md"})
			if tc.want == nil {
				if err == nil {
					t.Errorf("got %q, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
