package agent

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMark(t *testing.T) {
	run := t.TempDir()
	if err := os.WriteFile(filepath.Join(run, "nonce"), []byte("N0NCE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	a := &Agent{RunDir: run, Out: &out}
	a.mark("fold-start;⚙ x\a\x1b[1my") // a title from the model
	a.mark("fold-end")
	if got, want := out.String(), "\x1b]6973;N0NCE;fold-start;⚙ x[1my\a\x1b]6973;N0NCE;fold-end\a"; got != want {
		t.Fatalf("%q, want %q", got, want)
	}
}

func TestReadLine(t *testing.T) {
	r := strings.NewReader("y\nls\n")
	if got := readLine(r); got != "y" {
		t.Errorf("first line %q", got)
	}
	// What was typed after the answer is still there for whoever reads next.
	if rest, _ := io.ReadAll(r); string(rest) != "ls\n" {
		t.Errorf("left %q", rest)
	}
	if got := readLine(strings.NewReader("no newline")); got != "no newline" {
		t.Errorf("at EOF %q", got)
	}
}
