package config

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, src string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "aish.yaml")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad(t *testing.T) {
	secs, err := Load(write(t, "hello:\n  greeting: hi\n  retry:\n    max: 3\nempty:\n"))
	if err != nil {
		t.Fatal(err)
	}
	if g := secs["hello"]["greeting"]; g != "hi" {
		t.Errorf("greeting = %v", g)
	}
	if m := secs["hello"]["retry"].(map[string]any)["max"]; m != 3 {
		t.Errorf("retry.max = %v", m)
	}
	if s, ok := secs["empty"]; !ok || s != nil {
		t.Errorf("empty = %v, %v", s, ok)
	}
}

func TestLoadErrors(t *testing.T) {
	for name, path := range map[string]string{
		"scalar section": write(t, "hello: hi\n"),
		"list at top":    write(t, "- hello\n"),
		"duplicate":      write(t, "hello: {}\nhello: {}\n"),
		"missing file":   filepath.Join(t.TempDir(), "nope.yaml"),
	} {
		if _, err := Load(path); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if secs, err := Load(""); secs != nil || err != nil {
		t.Errorf("Load(\"\") = %v, %v", secs, err)
	}
}
