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
	first, err := c.Engine(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	ls := NewInput("bash", map[string]any{"command": "ls"}, dir)
	if d, _ := first.Check(ctx, ls); d.Action != Allow {
		t.Fatalf("no policies: %+v", d)
	}
	if again, _ := c.Engine(ctx, dir); again != first {
		t.Error("an unchanged directory was compiled again")
	}

	rego := filepath.Join(dir, "a.rego")
	write := func(src string) {
		t.Helper()
		if err := os.WriteFile(rego, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		// Two writes within one mtime tick would look the same; the test
		// must see every change.
		future := time.Now().Add(time.Duration(len(src)) * time.Second)
		os.Chtimes(rego, future, future)
	}
	write("package aish\n\ndecision := {\"action\": \"deny\", \"reason\": \"no\"} if input.tool == \"bash\"\n")
	second, err := c.Engine(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatal("a new file did not recompile")
	}
	if d, _ := second.Check(ctx, ls); d.Action != Deny {
		t.Errorf("new policy not in force: %+v", d)
	}

	write("package aish\n\ndecision := {\"action\": \"ask\"} if input.tool == \"bash\"\n")
	third, err := c.Engine(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := third.Check(ctx, ls); d.Action != Ask {
		t.Errorf("edited policy not in force: %+v", d)
	}

	write("package aish\n\ndecision := {broken\n")
	if _, err := c.Engine(ctx, dir); err == nil {
		t.Error("a broken policy compiled")
	}
	if _, err := c.Engine(ctx, dir); err == nil {
		t.Error("a broken policy is not tried again")
	}
}
