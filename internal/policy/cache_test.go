package policy

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCache(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	var c Cache
	first, err := c.Engine(ctx, dir, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	ls := callInput("bash", map[string]any{"command": "ls"}, dir)
	if d, _ := first.Check(ctx, ls); d.Action != Allow {
		t.Fatalf("no policies: %+v", d)
	}
	if again, _ := c.Engine(ctx, dir, Rules{}); again != first {
		t.Error("an unchanged directory was compiled again")
	}

	file := filepath.Join(dir, "a.cedar")
	write := func(src string) {
		t.Helper()
		if err := os.WriteFile(file, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		// Two writes within one mtime tick would look the same; the test
		// must see every change.
		future := time.Now().Add(time.Duration(len(src)) * time.Second)
		os.Chtimes(file, future, future)
	}
	if dirs, keys := c.Changed(); len(dirs)+len(keys) > 0 {
		t.Errorf("nothing changed, yet %q %q", dirs, keys)
	}

	// A new file is not in force until the cache is reset: the config is
	// applied by the user, all at once.
	write("permit(principal, action, resource);\n@reason(\"no\") forbid(principal, action, resource) when { context.tool == \"bash\" };\n")
	if again, _ := c.Engine(ctx, dir, Rules{}); again != first {
		t.Fatal("a new file was compiled before the reset")
	}
	dirs, keys := c.Changed()
	if len(dirs) != 1 || dirs[0] != dir || len(keys) != 1 {
		t.Fatalf("changed %q %q", dirs, keys)
	}
	fresh := new(Cache)
	second, err := fresh.Engine(ctx, dir, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	c.Reset(fresh)
	if again, _ := c.Engine(ctx, dir, Rules{}); again != second {
		t.Fatal("the policies compiled for the reset are not those in force")
	}
	if d, _ := second.Check(ctx, ls); d.Action != Deny {
		t.Errorf("new policy not in force: %+v", d)
	}
	if dirs, _ := c.Changed(); len(dirs) > 0 {
		t.Errorf("changed after the reset: %q", dirs)
	}

	// Each edit has a key of its own.
	write("permit(principal, action, resource);\n@ask(\"sure?\") forbid(principal, action, resource) when { context.tool == \"bash\" };\n")
	_, edited := c.Changed()
	write("permit(principal, action, resource);\n@ask(\"really sure?\") forbid(principal, action, resource) when { context.tool == \"bash\" };\n")
	if _, again := c.Changed(); len(again) != 1 || again[0] == edited[0] {
		t.Errorf("two edits, keys %q and %q", edited, again)
	}

	// A broken policy keeps nothing: tried again on the next call, it
	// compiles once mended.
	write("permit(principal, action, resource\n")
	var broken Cache
	if _, err := broken.Engine(ctx, dir, Rules{}); err == nil {
		t.Error("a broken policy compiled")
	}
	if _, err := broken.Engine(ctx, dir, Rules{}); err == nil {
		t.Error("a broken policy is not tried again")
	}
	write("permit(principal, action, resource);\n")
	if _, err := broken.Engine(ctx, dir, Rules{}); err != nil {
		t.Errorf("mended: %v", err)
	}
}

func TestCacheRules(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	var c Cache
	sudo := callInput("bash", map[string]any{"command": "sudo ls"}, dir)
	first, err := c.Engine(ctx, dir, Rules{Ask: []string{"sudo *"}})
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := first.Check(ctx, sudo); d.Action != Ask {
		t.Fatalf("ask rule: %+v", d)
	}
	if again, _ := c.Engine(ctx, dir, Rules{Ask: []string{"sudo *"}}); again != first {
		t.Error("the same rules were compiled again")
	}
	// The same pattern in the other list is another policy.
	second, err := c.Engine(ctx, dir, Rules{Deny: []string{"sudo *"}})
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := second.Check(ctx, sudo); d.Action != Deny {
		t.Errorf("edited rules not in force: %+v", d)
	}
	if _, err := c.Engine(ctx, dir, Rules{WriteOutsideHome: "never"}); err == nil {
		t.Error("an unknown write_outside_home loaded")
	}
}
