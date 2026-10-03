package config

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// readmeHidden are the keys of Config, dotted, that the config.toml
// example of the README leaves out on purpose, each with the reason.
var readmeHidden = map[string]string{}

// TestReadmeExample keeps the config.toml example of the README whole:
// it promises every key, and a new one is easily forgotten there. It must
// also load as config.toml does: a top-level key written after a table
// lands inside it, an unknown key then.
func TestReadmeExample(t *testing.T) {
	block := readmeExample(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(block), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AISH_CONFIG", path)
	t.Setenv("AISH_PROFILE", "")
	t.Setenv("AISH_MODEL", "")
	t.Setenv("AISH_EFFORT", "")
	if _, err := Load(); err != nil {
		t.Fatalf("README.md example: %v", err)
	}
	var raw map[string]any
	md, err := toml.Decode(block, &raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range tomlKeys(reflect.TypeFor[Config](), nil) {
		name := strings.Join(key, ".")
		if _, ok := readmeHidden[name]; ok {
			continue
		}
		if !md.IsDefined(key...) {
			t.Errorf("README.md example has no key %s", name)
		}
	}
}

// readmeExample is the first toml block of the section "## Настройки".
func readmeExample(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	_, section, ok := strings.Cut(string(data), "\n## Настройки\n")
	if !ok {
		t.Fatal("README.md: no section ## Настройки")
	}
	section, _, _ = strings.Cut(section, "\n## ")
	_, block, ok := strings.Cut(section, "\n```toml\n")
	if !ok {
		t.Fatal("README.md: no toml block in ## Настройки")
	}
	block, _, ok = strings.Cut(block, "\n```\n")
	if !ok {
		t.Fatal("README.md: the toml block of ## Настройки is not closed")
	}
	return block + "\n"
}

// tomlKeys are the keys of struct type t, each a path through the tables
// it is in. A struct field is a table of its own keys; a map is a table
// whose keys the user names, and so stands for itself.
func tomlKeys(t reflect.Type, prefix []string) [][]string {
	var keys [][]string
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("toml"), ",")
		if name == "-" || !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		key := slices.Concat(prefix, []string{name})
		ft := f.Type
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct {
			keys = append(keys, tomlKeys(ft, key)...)
			continue
		}
		keys = append(keys, key)
	}
	return keys
}
