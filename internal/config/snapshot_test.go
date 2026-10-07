package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// snapshotOf writes toml as config.toml and takes a snapshot of it.
func snapshotOf(t *testing.T, toml string) (*Snapshot, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	t.Setenv("AISH_CONFIG", path)
	if err := os.WriteFile(path, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	return NewSnapshot(), path
}

// A snapshot is config.toml as it was read: an edit is in the next one.
func TestSnapshot(t *testing.T) {
	s, path := snapshotOf(t, profiles)
	if s.Path() != path || len(s.Changed("/")) > 0 || len(s.Stale()) > 0 {
		t.Fatalf("path %q, changed %q, stale %q", s.Path(), s.Changed("/"), s.Stale())
	}
	edited := "fold_lines = 7\n" + profiles
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := s.LoadEnv(func(string) string { return "" })
	if err != nil || cfg.FoldLines != 0 || cfg.Profile != "work" || cfg.Model != "claude-opus-5" {
		t.Errorf("the snapshot: %v %+v", err, cfg)
	}
	if cfg, err := s.LoadProfile("local"); err != nil || cfg.Model != "qwen3:32b" || cfg.FoldLines != 0 {
		t.Errorf("a profile of the snapshot: %v %+v", err, cfg)
	}
	changed := s.Changed("/")
	if len(changed) != 1 || !slices.Equal(s.Stale(), []string{path}) {
		t.Errorf("changed %q, stale %q", changed, s.Stale())
	}
	if err := os.WriteFile(path, []byte("markdown = false\n"+edited), 0o600); err != nil {
		t.Fatal(err)
	}
	if again := s.Changed("/"); len(again) != 1 || again[0] == changed[0] {
		t.Errorf("another edit, keys %q and %q", changed, again)
	}

	next := NewSnapshot()
	if cfg, err := next.LoadEnv(func(string) string { return "" }); err != nil || cfg.FoldLines != 7 || cfg.Markdown {
		t.Errorf("the next snapshot: %v %+v", err, cfg)
	}
	if keys := s.Keys(next); !slices.Equal(keys, []string{"fold_lines", "markdown"}) {
		t.Errorf("keys %q", keys)
	}
}

// Keys names what changed by its dotted path, and a table that came or went
// by its own.
func TestSnapshotKeys(t *testing.T) {
	s, path := snapshotOf(t, profiles)
	edited := `
model = "top"
effort = "high"
max_tokens = 1000
profile = "work"
journal_ignore = ["env"]

[profiles.work]
provider = "anthropic"
base_url = "http://127.0.0.1:8318"
model = "claude-opus-5"

[profiles.top]

[profiles.lan]
model = "x"

[policy]
deny = ["rm *"]
`
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	want := []string{"journal_ignore", "policy", "profiles.lan", "profiles.local", "profiles.work.base_url"}
	if keys := s.Keys(NewSnapshot()); !slices.Equal(keys, want) {
		t.Errorf("keys %q, want %q", keys, want)
	}
	if keys := s.Keys(s); len(keys) > 0 {
		t.Errorf("the same file: %q", keys)
	}
}

// A file that is not there is the defaults, in the snapshot as in Load; one
// that comes later is a change.
func TestSnapshotNoFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	t.Setenv("AISH_CONFIG", path)
	s := NewSnapshot()
	if cfg, err := s.LoadProfile(""); err != nil || cfg.Model != Default().Model {
		t.Errorf("no file: %v %+v", err, cfg)
	}
	if len(s.Changed("/")) > 0 {
		t.Error("no file either time is a change")
	}
	if err := os.WriteFile(path, []byte("model = [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if len(s.Changed("/")) != 1 {
		t.Error("a new file is no change")
	}
	if _, err := NewSnapshot().LoadProfile(""); err == nil {
		t.Error("a broken file loaded")
	}
}

// The project file of a directory is read once and kept: a new one, or an
// edit, is in the next snapshot. Its code is not kept: an edit turns it
// off at once, and so does taking the trust back.
func TestSnapshotProject(t *testing.T) {
	root := trustHome(t)
	t.Setenv("AISH_CONFIG", filepath.Join(root, "none.toml"))
	top := filepath.Join(root, "home", "src", "x")
	sub := filepath.Join(top, "sub")
	if err := os.MkdirAll(filepath.Join(top, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	s := NewSnapshot()
	if _, file, err := s.Project(Default(), sub); file != "" || err != nil {
		t.Fatalf("no file yet: %q %v", file, err)
	}
	file := filepath.Join(top, ProjectFile)
	write := func(toml string) {
		t.Helper()
		if err := os.WriteFile(file, []byte(toml), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("max_steps = 7\nhooks_dir = \"hooks\"\n")
	if err := Trust(file); err != nil {
		t.Fatal(err)
	}
	if _, got, _ := s.Project(Default(), sub); got != "" {
		t.Errorf("a file new in a directory asked about: %q", got)
	}
	if !slices.Equal(s.Stale(), []string{file}) || len(s.Changed(sub)) != 1 || len(s.Changed(top)) > 0 {
		t.Errorf("stale %q, changed %q", s.Stale(), s.Changed(sub))
	}

	s = NewSnapshot()
	cfg, got, err := s.Project(Default(), sub)
	hooks := Default().HooksDir + string(filepath.ListSeparator) + filepath.Join(top, "hooks")
	if err != nil || got != file || cfg.MaxSteps != 7 || cfg.HooksDir != hooks {
		t.Fatalf("trusted: %q %v %+v", got, err, cfg)
	}
	// Asked from another directory of the project, the same file as read.
	write("max_steps = 9\nhooks_dir = \"hooks\"\n")
	cfg, _, _ = s.Project(Default(), top)
	if cfg.MaxSteps != 7 || cfg.HooksDir != Default().HooksDir || !slices.Equal(cfg.Untrusted, []string{"hooks_dir"}) {
		t.Errorf("edited: %+v", cfg)
	}
	if !slices.Equal(s.Stale(), []string{file}) || len(s.Changed(sub)) != 1 {
		t.Errorf("stale %q, changed %q", s.Stale(), s.Changed(sub))
	}
	// Trusted as it is now: still not the contents read.
	if err := Trust(file); err != nil {
		t.Fatal(err)
	}
	if cfg, _, _ = s.Project(Default(), sub); len(cfg.Untrusted) == 0 {
		t.Errorf("the edit trusted holds for the file read: %+v", cfg)
	}
	// Back as read: trusted, as it was, if the trust is for it.
	write("max_steps = 7\nhooks_dir = \"hooks\"\n")
	if cfg, _, _ = s.Project(Default(), sub); len(cfg.Untrusted) == 0 {
		t.Errorf("trusted for another contents: %+v", cfg)
	}
	if err := Trust(file); err != nil {
		t.Fatal(err)
	}
	if cfg, _, _ = s.Project(Default(), sub); cfg.HooksDir != hooks {
		t.Errorf("trusted again: %+v", cfg)
	}
	if err := Untrust(file); err != nil {
		t.Fatal(err)
	}
	if cfg, _, _ = s.Project(Default(), sub); cfg.HooksDir != Default().HooksDir {
		t.Errorf("revoked: %+v", cfg)
	}
	// Gone from the disk, its code with it.
	if err := Trust(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if cfg, _, _ = s.Project(Default(), sub); cfg.MaxSteps != 7 || cfg.HooksDir != Default().HooksDir {
		t.Errorf("removed: %+v", cfg)
	}
}

// A FIFO in place of config.toml or of the project file read keeps no
// request waiting for a writer.
func TestSnapshotFIFO(t *testing.T) {
	root := trustHome(t)
	path := filepath.Join(root, "config.toml")
	t.Setenv("AISH_CONFIG", path)
	top := filepath.Join(root, "home", "x")
	repo(t, top, "max_steps = 7\n")
	s := NewSnapshot()
	if _, file, _ := s.Project(Default(), top); file == "" {
		t.Fatal("no project file")
	}
	mkfifo(t, path)
	if err := os.Remove(filepath.Join(top, ProjectFile)); err != nil {
		t.Fatal(err)
	}
	mkfifo(t, filepath.Join(top, ProjectFile))
	changed := quick(t, path, func() []string { return s.Changed(top) })
	if len(changed) != 2 {
		t.Errorf("changed %q", changed)
	}
	quick(t, path, func() []string { return s.Stale() })
	cfg, _, _ := quick(t, path, func() found {
		c, f, err := s.Project(Default(), top)
		return found{c, f, err}
	}).unpack()
	if cfg.MaxSteps != 7 {
		t.Errorf("the file read: %+v", cfg)
	}
}

func (f found) unpack() (Config, string, error) { return f.cfg, f.file, f.err }
