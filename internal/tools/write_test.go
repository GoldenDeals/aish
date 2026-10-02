package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// noTemp fails the test if a temporary file of writeAtomic is left in dir.
func noTemp(t *testing.T, dir string) {
	t.Helper()
	if left, _ := filepath.Glob(filepath.Join(dir, ".*.aish-*")); len(left) > 0 {
		t.Errorf("temporary files left: %v", left)
	}
}

func isLink(t *testing.T, path string) bool {
	t.Helper()
	st, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode()&os.ModeSymlink != 0
}

func TestWriteHome(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	write, _ := Load("").Get("write_file")
	for _, p := range []string{"~/x", "~/sub/../y"} {
		if _, err := write.Execute(context.Background(), Exec{Dir: cwd}, map[string]any{"path": p, "content": "hi"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if readBack(t, filepath.Join(home, "x")) != "hi" || readBack(t, filepath.Join(home, "y")) != "hi" {
		t.Error("not written to $HOME")
	}
	if _, err := os.Stat(filepath.Join(cwd, "~")); err == nil {
		t.Error("~ taken as a directory in cwd")
	}
}

func TestWriteThroughLink(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	real, link := filepath.Join(dir, "real"), filepath.Join(dir, "link")
	if err := os.WriteFile(real, []byte("old a\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	// Past the umask: an existing file keeps its mode, whatever the umask.
	if err := os.Chmod(real, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", link); err != nil {
		t.Fatal(err)
	}
	if _, err := writeFile(ctx, map[string]any{"path": link, "content": "new a\n"}); err != nil {
		t.Fatal(err)
	}
	if _, err := editFile(ctx, map[string]any{"path": link, "old_string": "a", "new_string": "b"}); err != nil {
		t.Fatal(err)
	}
	if got := readBack(t, real); got != "new b\n" {
		t.Errorf("target %q", got)
	}
	if !isLink(t, link) {
		t.Error("the link was replaced by a file")
	}
	if st := must(os.Stat(real)); st.Mode().Perm() != 0o640 {
		t.Errorf("target mode %v, want 0640", st.Mode().Perm())
	}

	// A dangling link creates its target, relative to where the link is.
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	dangling := filepath.Join(dir, "sub", "dangling")
	if err := os.Symlink("../new", dangling); err != nil {
		t.Fatal(err)
	}
	if _, err := writeFile(ctx, map[string]any{"path": dangling, "content": "created"}); err != nil {
		t.Fatal(err)
	}
	if got := readBack(t, filepath.Join(dir, "new")); got != "created" {
		t.Errorf("dangling link target %q", got)
	}
	if !isLink(t, dangling) {
		t.Error("the dangling link was replaced by a file")
	}
	noTemp(t, dir)
	noTemp(t, filepath.Join(dir, "sub"))

	// ".." in a link's target goes up from where the link before it leads,
	// as with the kernel and the policy, not back over its name.
	other := t.TempDir()
	if err := os.MkdirAll(filepath.Join(other, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(other, "a", "b"), filepath.Join(dir, "deep")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("deep/../up", filepath.Join(dir, "up")); err != nil {
		t.Fatal(err)
	}
	if _, err := writeFile(ctx, map[string]any{"path": filepath.Join(dir, "up"), "content": "up"}); err != nil {
		t.Fatal(err)
	}
	if got := readBack(t, filepath.Join(other, "a", "up")); got != "up" {
		t.Errorf("through deep/..: %q", got)
	}
	if !isLink(t, filepath.Join(dir, "up")) {
		t.Error("the link was replaced by a file")
	}
	noTemp(t, filepath.Join(other, "a"))
}

func TestWriteAtomicFails(t *testing.T) {
	dir := t.TempDir()
	// A loop of links fails as the kernel would, and creates nothing.
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	if err := os.Symlink("b", a); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a", b); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(a, []byte("x"), 0o644); !errors.Is(err, syscall.ELOOP) {
		t.Errorf("loop of links: %v", err)
	}
	if !isLink(t, a) || !isLink(t, b) {
		t.Error("a link of the loop was replaced")
	}
	// A directory is not replaced, and the temporary file goes away.
	if err := os.Mkdir(filepath.Join(dir, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(filepath.Join(dir, "d"), []byte("x"), 0o644); err == nil {
		t.Error("replaced a directory")
	}
	noTemp(t, dir)
	// A read-only file is refused, as an in-place write would be.
	if os.Geteuid() != 0 {
		ro := filepath.Join(dir, "ro")
		if err := os.WriteFile(ro, []byte("keep"), 0o444); err != nil {
			t.Fatal(err)
		}
		if _, err := writeFile(context.Background(), map[string]any{"path": ro, "content": "x"}); !errors.Is(err, os.ErrPermission) {
			t.Errorf("read-only file: %v", err)
		}
		if readBack(t, ro) != "keep" {
			t.Error("read-only file replaced")
		}
		noTemp(t, dir)
	}
}
