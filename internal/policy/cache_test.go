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
	ls := NewInput("bash", map[string]any{"command": "ls"}, dir)
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
	write("permit(principal, action, resource);\n@reason(\"no\") forbid(principal, action, resource) when { context.tool == \"bash\" };\n")
	second, err := c.Engine(ctx, dir, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatal("a new file did not recompile")
	}
	if d, _ := second.Check(ctx, ls); d.Action != Deny {
		t.Errorf("new policy not in force: %+v", d)
	}

	write("permit(principal, action, resource);\n@ask(\"sure?\") forbid(principal, action, resource) when { context.tool == \"bash\" };\n")
	third, err := c.Engine(ctx, dir, Rules{})
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := third.Check(ctx, ls); d.Action != Ask {
		t.Errorf("edited policy not in force: %+v", d)
	}

	write("permit(principal, action, resource\n")
	if _, err := c.Engine(ctx, dir, Rules{}); err == nil {
		t.Error("a broken policy compiled")
	}
	if _, err := c.Engine(ctx, dir, Rules{}); err == nil {
		t.Error("a broken policy is not tried again")
	}
}

func TestCacheRules(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	var c Cache
	sudo := NewInput("bash", map[string]any{"command": "sudo ls"}, dir)
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
