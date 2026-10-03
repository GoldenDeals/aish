package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A trusted project file replaced by a FIFO is not trusted, and asking
// does not wait for a writer: Trusted runs on every request, Sum for
// every file of aish trust --list.
func TestTrustFIFO(t *testing.T) {
	root := trustHome(t)
	path := filepath.Join(root, ProjectFile)
	if err := os.WriteFile(path, []byte("hooks_dir = \"hooks\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Trust(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	mkfifo(t, path)
	link := filepath.Join(root, "link", ProjectFile)
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{path, link} {
		if quick(t, path, func() bool { return Trusted(p) }) {
			t.Errorf("%s: a FIFO trusted", p)
		}
		if sum := quick(t, path, func() string { return Sum(p) }); sum != "" {
			t.Errorf("%s: sum %q of a FIFO", p, sum)
		}
		if err := quick(t, path, func() error { return Trust(p) }); err == nil || !strings.Contains(err.Error(), p+": not a regular file") {
			t.Errorf("trust %s: %v", p, err)
		}
		if err := quick(t, path, func() error { _, err := CodeKeys(p); return err }); err == nil || !strings.Contains(err.Error(), p+": not a regular file") {
			t.Errorf("CodeKeys %s: %v", p, err)
		}
	}
	if _, ok := TrustedFiles()[path]; !ok {
		t.Error("the trust of the file was lost")
	}
}

// A TrustFile replaced by a FIFO trusts nothing, at once.
func TestTrustFileFIFO(t *testing.T) {
	root := trustHome(t)
	path := filepath.Join(root, ProjectFile)
	if err := os.WriteFile(path, []byte("hooks_dir = \"hooks\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Trust(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(TrustFile()); err != nil {
		t.Fatal(err)
	}
	mkfifo(t, TrustFile())
	if all := quick(t, TrustFile(), TrustedFiles); len(all) != 0 {
		t.Errorf("trusted from a FIFO: %v", all)
	}
	if quick(t, TrustFile(), func() bool { return Trusted(path) }) {
		t.Error("trusted with a FIFO for the TrustFile")
	}
}
