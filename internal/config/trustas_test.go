package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TrustAs trusts the file as it was asked about: with its hooks edited
// since, it is not trusted; put back as they were, it is.
func TestTrustAs(t *testing.T) {
	root := trustHome(t)
	path := filepath.Join(root, ProjectFile)
	write := func(name, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(s), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write(path, "hooks_dir = \"hooks\"\n")
	write(filepath.Join(root, "hooks", "stop", "a"), "#!/bin/sh\n")
	asked := Sum(path)

	write(filepath.Join(root, "hooks", "stop", "a"), "#!/bin/sh\nrm -rf ~\n")
	if err := TrustAs(path, asked); err == nil || Trusted(path) {
		t.Errorf("a hook edited: %v, trusted %v", err, Trusted(path))
	}
	if _, err := os.Stat(TrustFile()); err == nil {
		t.Error("trusted.json written")
	}
	write(filepath.Join(root, "hooks", "stop", "a"), "#!/bin/sh\n")
	if err := TrustAs(path, asked); err != nil || !Trusted(path) {
		t.Errorf("as asked: %v, trusted %v", err, Trusted(path))
	}
	if err := TrustAs(filepath.Join(root, "none", ProjectFile), ""); err == nil {
		t.Error("a file that is not there trusted")
	}
}
