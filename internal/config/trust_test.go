package config

import (
	"os"
	"path/filepath"
	"testing"
)

// trustHome keeps TrustFile in a directory of the test's own.
func trustHome(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	return root
}

func TestTrust(t *testing.T) {
	root := trustHome(t)
	path := filepath.Join(root, ProjectFile)
	if err := os.WriteFile(path, []byte("hooks_dir = \".aish/hooks\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if Trusted(path) {
		t.Fatal("trusted before Trust")
	}
	if err := Trust(path); err != nil {
		t.Fatal(err)
	}
	if !Trusted(path) {
		t.Fatal("not trusted after Trust")
	}
	st, err := os.Stat(TrustFile())
	if err != nil {
		t.Fatal(err)
	}
	if mode := st.Mode().Perm(); mode != 0o600 {
		t.Errorf("%s is %o, want 600", TrustFile(), mode)
	}
	if st, err := os.Stat(filepath.Dir(TrustFile())); err != nil || st.Mode().Perm() != 0o700 {
		t.Errorf("its directory: %v, %v", st.Mode(), err)
	}
	if left, _ := filepath.Glob(TrustFile() + ".*"); len(left) > 0 {
		t.Errorf("temporary files left: %v", left)
	}

	// Another way to the same directory is the same file.
	link := filepath.Join(root, "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if !Trusted(filepath.Join(link, ProjectFile)) {
		t.Error("not trusted through a link to its directory")
	}

	// An edit takes the trust back, the old contents bring it back.
	if err := os.WriteFile(path, []byte("hooks_dir = \"evil\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if Trusted(path) {
		t.Error("trusted after an edit")
	}
	if err := os.WriteFile(path, []byte("hooks_dir = \".aish/hooks\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !Trusted(path) {
		t.Error("not trusted with the contents trusted")
	}

	if err := Untrust(path); err != nil {
		t.Fatal(err)
	}
	if Trusted(path) {
		t.Error("trusted after Untrust")
	}
	if err := Untrust(path); err != nil {
		t.Errorf("Untrust of a file not trusted: %v", err)
	}
}

// A link to a trusted file brings no trust to the directory it is in: the
// hooks it names are taken from there.
func TestTrustLinkedFile(t *testing.T) {
	root := trustHome(t)
	path := filepath.Join(root, ProjectFile)
	if err := os.WriteFile(path, []byte("hooks_dir = \".aish/hooks\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Trust(path); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(root, "other")
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path, filepath.Join(other, ProjectFile)); err != nil {
		t.Fatal(err)
	}
	if Trusted(filepath.Join(other, ProjectFile)) {
		t.Error("a link to a trusted file is trusted in another directory")
	}
}

// A TrustFile that cannot be read is none trusted, not an error: Trust
// writes it anew.
func TestTrustBroken(t *testing.T) {
	root := trustHome(t)
	path := filepath.Join(root, ProjectFile)
	if err := os.WriteFile(path, []byte("max_steps = 3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Trust(path); err != nil {
		t.Fatal(err)
	}
	for _, broken := range []string{"{", "null", `{"` + path + `": 1}`, "[]"} {
		if err := os.WriteFile(TrustFile(), []byte(broken), 0o600); err != nil {
			t.Fatal(err)
		}
		if Trusted(path) {
			t.Errorf("%q: trusted", broken)
		}
		if n := len(TrustedFiles()); n != 0 {
			t.Errorf("%q: %d files", broken, n)
		}
	}
	if err := Trust(path); err != nil || !Trusted(path) {
		t.Errorf("Trust over a broken file: %v", err)
	}
}

func TestCodeKeys(t *testing.T) {
	root := trustHome(t)
	path := filepath.Join(root, ProjectFile)
	if err := os.WriteFile(path, []byte("max_steps = 3\ntools_dir = \"tools\"\nhooks_dir = \".aish/hooks\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	keys, err := CodeKeys(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0] != `hooks_dir = ".aish/hooks"` || keys[1] != `tools_dir = "tools"` {
		t.Errorf("keys %q", keys)
	}
}
