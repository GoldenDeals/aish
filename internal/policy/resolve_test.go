package policy

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// links makes a home and an out directory side by side in a temporary
// directory of real names, and the symlinks given as name → target in
// home.
func links(t *testing.T, ls map[string]string) (home, out string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home, out = filepath.Join(root, "home"), filepath.Join(root, "out")
	for _, d := range []string{home, out, filepath.Join(out, "deep", "er")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, target := range ls {
		target = os.Expand(target, func(v string) string { return map[string]string{"home": home, "out": out}[v] })
		if err := os.Symlink(target, filepath.Join(home, name)); err != nil {
			t.Fatal(err)
		}
	}
	return home, out
}

func TestResolve(t *testing.T) {
	home, out := links(t, map[string]string{
		"dangling": "$out/new",
		"up":       "../out",
		"updang":   "../out/new",
		"chain":    "dangling",
		"deep":     "$out/deep/er",
		"a":        "b",
		"b":        "a",
		"self":     "self",
	})
	// A relative target is taken from where the link really is: deep/back
	// is in out/deep/er, so .. is out/deep, not home.
	if err := os.Symlink("../back", filepath.Join(out, "deep", "er", "back")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "file"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ path, want string }{
		{"dangling", out + "/new"},
		{"dangling/x/y", out + "/new/x/y"},
		{"up", out},
		{"up/x", out + "/x"},
		{"updang", out + "/new"},
		{"chain", out + "/new"},
		{"deep/back", out + "/deep/back"},
		{"deep/back/x", out + "/deep/back/x"},
		// Existing paths and new ones under them, as before.
		{"", home},
		{"file", home + "/file"},
		{"new/dir/f", home + "/new/dir/f"},
		{"deep/f", out + "/deep/er/f"},
		{"up/../x", home + "/x"}, // cleaned first, as the tools clean it
	} {
		if got := resolve(home + "/" + c.path); got != c.want {
			t.Errorf("resolve(home/%s) = %s, want %s", c.path, got, c.want)
		}
	}
	for _, p := range []string{home, filepath.Join(home, "file"), filepath.Join(home, "up"), filepath.Join(home, "deep")} {
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			t.Fatal(err)
		}
		if got := resolve(p); got != real {
			t.Errorf("resolve(%s) = %s, EvalSymlinks %s", p, got, real)
		}
	}
	// A loop ends as the kernel ends it, on a name of the loop.
	for _, p := range []string{"a", "a/x", "self"} {
		got := resolve(filepath.Join(home, p))
		if filepath.Dir(got) != home && filepath.Dir(filepath.Dir(got)) != home {
			t.Errorf("resolve(home/%s) = %s, want a name in home", p, got)
		}
	}
	if got := resolve("/"); got != "/" {
		t.Errorf("resolve(/) = %s", got)
	}
}

func TestInputHome(t *testing.T) {
	home, _ := links(t, nil)
	t.Setenv("HOME", home)
	for _, c := range []struct{ path, want string }{
		{"~/x", home + "/x"},
		{"~", home},
		{"~x", "/w/~x"},
		{"a/~/x", "/w/a/~/x"},
	} {
		if got := NewInput("write_file", map[string]any{"path": c.path}, "/w").Path; got != c.want {
			t.Errorf("path %s: %s, want %s", c.path, got, c.want)
		}
	}
}

func TestDanglingLinkOutOfHome(t *testing.T) {
	ctx := context.Background()
	home, out := links(t, map[string]string{
		"dangling": "$out/new",
		"updang":   "../out/new",
		"inside":   "$home/new",
	})
	t.Setenv("HOME", home)
	e, err := Load(ctx, t.TempDir(), Rules{WriteOutsideHome: Deny})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ path, want, reason string }{
		{"dangling", Deny, "writes outside home: " + out + "/new"},
		{"~/dangling", Deny, "writes outside home: " + out + "/new"},
		{"updang", Deny, "writes outside home: " + out + "/new"},
		{"inside", Allow, ""},
	} {
		d, err := e.Check(ctx, NewInput("write_file", map[string]any{"path": c.path}, home))
		if err != nil {
			t.Fatal(err)
		}
		if d.Action != c.want || d.Reason != c.reason {
			t.Errorf("write_file %s: %s (%s), want %s (%s)", c.path, d.Action, d.Reason, c.want, c.reason)
		}
	}
}
