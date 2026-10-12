package edit

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, []byte("x = 1\ny = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := run(context.Background(), input{Path: path, Old: "= 1", New: "= 2"}); err == nil {
		t.Error("an ambiguous edit is made")
	}
	if _, err := run(context.Background(), input{Path: path, Old: "z", New: "w"}); err == nil {
		t.Error("an edit of missing text is made")
	}
	if _, err := run(context.Background(), input{Path: path, Old: "y = 1", New: "y = 2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := run(context.Background(), input{Path: path, Old: "= ", New: ":= ", ReplaceAll: true}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "x := 1\ny := 2\n" {
		t.Errorf("file = %q", data)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want it kept", fi.Mode())
	}
}
