package read

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, []byte("a\nb\nc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		in   input
		want string
	}{
		{input{Path: path}, "     1\ta\n     2\tb\n     3\tc\n"},
		{input{Path: path, Offset: 2, Limit: 1}, "     2\tb\n(1 more lines: read on from offset 3)\n"},
		{input{Path: path, Offset: 4}, "offset 4 is past the end: the file has 3 lines"},
	} {
		got, err := run(context.Background(), tc.in)
		if err != nil {
			got = err.Error()
		}
		if got != tc.want {
			t.Errorf("%+v = %q, want %q", tc.in, got, tc.want)
		}
	}
}
