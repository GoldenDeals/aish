package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func readBack(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestEditFile(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "f.sh")
	const orig = "a := 1\nb := a\nc := a\n"
	for _, tc := range []struct {
		name     string
		args     map[string]any
		want     string
		msg, err string
	}{
		{"one", map[string]any{"old_string": "b := a", "new_string": "b := 2"}, "a := 1\nb := 2\nc := a\n", "replaced 1 occurrence(s)", ""},
		{"none", map[string]any{"old_string": "d :=", "new_string": "x"}, orig, "", "old_string not found in "},
		{"several", map[string]any{"old_string": "a", "new_string": "z"}, orig, "", "old_string occurs 3 times in "},
		{"replace_all", map[string]any{"old_string": "a", "new_string": "z", "replace_all": true}, "z := 1\nb := z\nc := z\n", "replaced 3 occurrence(s)", ""},
		{"replace_all from the CLI", map[string]any{"old_string": "a", "new_string": "z", "replace_all": "true"}, "z := 1\nb := z\nc := z\n", "replaced 3", ""},
		{"delete", map[string]any{"old_string": "c := a\n", "new_string": ""}, "a := 1\nb := a\n", "replaced 1", ""},
		{"empty old_string", map[string]any{"old_string": "", "new_string": "x"}, orig, "", "old_string is empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeExec(t, path, orig, 0o750)
			tc.args["path"] = path
			msg, err := editFile(ctx, tc.args)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Errorf("error %v, want %q", err, tc.err)
				}
			} else if err != nil {
				t.Fatal(err)
			} else if !strings.Contains(msg, tc.msg) {
				t.Errorf("message %q, want %q", msg, tc.msg)
			}
			if got := readBack(t, path); got != tc.want {
				t.Errorf("file:\n%s\nwant:\n%s", got, tc.want)
			}
			if st, _ := os.Stat(path); st.Mode().Perm() != 0o750 {
				t.Errorf("mode %v, want 0750", st.Mode().Perm())
			}
		})
	}

	if _, err := editFile(ctx, map[string]any{"path": path + ".missing", "old_string": "a"}); !os.IsNotExist(err) {
		t.Errorf("missing file: %v", err)
	}
}

func TestWriteFile(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "new", "dir", "f")
	msg, err := writeFile(ctx, map[string]any{"path": path, "content": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if msg != "wrote 5 bytes to "+path || readBack(t, path) != "hello" {
		t.Errorf("message %q, content %q", msg, readBack(t, path))
	}
	ref := filepath.Join(filepath.Dir(path), "ref")
	if err := os.WriteFile(ref, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if st, rst := must(os.Stat(path)), must(os.Stat(ref)); st.Mode() != rst.Mode() {
		t.Errorf("new file mode %v, want 0644 under the umask: %v", st.Mode(), rst.Mode())
	}

	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := writeFile(ctx, map[string]any{"path": path, "content": ""}); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 || st.Size() != 0 {
		t.Errorf("rewritten file: mode %v, size %d", st.Mode().Perm(), st.Size())
	}
}

func TestReadFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	var b strings.Builder
	for i := 1; i <= 5; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	writeExec(t, path, b.String(), 0o644)

	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{}, "     1\tline 1\n     2\tline 2\n     3\tline 3\n     4\tline 4\n     5\tline 5\n"},
		{map[string]any{"offset": 4.0}, "     4\tline 4\n     5\tline 5\n"},
		{map[string]any{"offset": 2, "limit": "2"}, "     2\tline 2\n     3\tline 3\n[... more lines; continue with offset=4]\n"},
		{map[string]any{"limit": 5}, "     1\tline 1\n     2\tline 2\n     3\tline 3\n     4\tline 4\n     5\tline 5\n"},
		{map[string]any{"offset": 9}, ""},
	} {
		tc.args["path"] = path
		got, err := readFile(ctx, tc.args)
		if err != nil || got != tc.want {
			t.Errorf("%v: got %q, %v\nwant %q", tc.args, got, err, tc.want)
		}
	}

	long := filepath.Join(dir, "long")
	writeExec(t, long, strings.Repeat("x", 2500)+"\n", 0o644)
	if got, _ := readFile(ctx, map[string]any{"path": long}); got != "     1\t"+strings.Repeat("x", 2000)+"[...]\n" {
		t.Errorf("long line: %d bytes, %q…", len(got), got[len(got)-10:])
	}
	// "я" is two bytes, so byte 2000 falls inside one.
	writeExec(t, long, "x"+strings.Repeat("я", 1500)+"\n", 0o644)
	if got, _ := readFile(ctx, map[string]any{"path": long}); got != "     1\tx"+strings.Repeat("я", 999)+"[...]\n" {
		t.Errorf("long line cut inside a rune: %d bytes, %q…", len(got), got[len(got)-10:])
	}

	empty := filepath.Join(dir, "empty")
	writeExec(t, empty, "", 0o644)
	if got, err := readFile(ctx, map[string]any{"path": empty}); got != "(empty file)" || err != nil {
		t.Errorf("empty file: %q, %v", got, err)
	}
	if _, err := readFile(ctx, map[string]any{"path": filepath.Join(dir, "missing")}); !os.IsNotExist(err) {
		t.Errorf("missing file: %v", err)
	}
}
