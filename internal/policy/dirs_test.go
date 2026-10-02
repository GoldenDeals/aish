package policy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// policyDir writes the files into a fresh directory and returns it.
func policyDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func loadDirs(t *testing.T, dirs ...string) *Engine {
	t.Helper()
	e, err := Load(context.Background(), strings.Join(dirs, string(filepath.ListSeparator)), Rules{})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func wantVerdicts(t *testing.T, e *Engine, want map[string]string) {
	t.Helper()
	for cmd, action := range want {
		if d := check(t, e, bash(cmd)); d.Action != action {
			t.Errorf("%s: %+v, want %s", cmd, d, action)
		}
	}
}

// A project's permit must not open what the user's set keeps closed by
// not permitting it.
func TestPermitOfOtherDir(t *testing.T) {
	a := policyDir(t, map[string]string{"user.cedar": `permit(principal, action == Action::"run", resource == Command::"ls");` + "\n"})
	b := policyDir(t, map[string]string{"x.cedar": permitAll})
	e := loadDirs(t, a, b)
	wantVerdicts(t, e, map[string]string{"rm x": Deny, "ls": Allow})
	if n := len(e.Summary()); n != 2 {
		t.Errorf("summary lists %d files, want 2: %+v", n, e.Summary())
	}
}

// Every set is default deny: a directory of forbids alone permits nothing.
func TestForbidOnlyDir(t *testing.T) {
	forbid := `@reason("no rm") forbid(principal, action == Action::"run", resource == Command::"rm");` + "\n"
	a := policyDir(t, map[string]string{"user.cedar": permitAll})
	b := policyDir(t, map[string]string{"x.cedar": forbid})
	wantVerdicts(t, loadDirs(t, a, b), map[string]string{"ls": Deny, "rm x": Deny})

	b = policyDir(t, map[string]string{"x.cedar": permitAll + forbid})
	e := loadDirs(t, a, b)
	wantVerdicts(t, e, map[string]string{"ls": Allow, "rm x": Deny})
	if d := check(t, e, bash("rm x")); d.Reason != "no rm" {
		t.Errorf("rm x: reason %q, want the project's", d.Reason)
	}
}

// The same file name in two directories is two sets, not a collision.
func TestSameFileInTwoDirs(t *testing.T) {
	a := policyDir(t, map[string]string{"p.cedar": permitAll + `@reason("a") forbid(principal, action == Action::"run", resource == Command::"a");` + "\n"})
	b := policyDir(t, map[string]string{"p.cedar": permitAll + `@reason("b") forbid(principal, action == Action::"run", resource == Command::"b");` + "\n"})
	wantVerdicts(t, loadDirs(t, a, b), map[string]string{"a": Deny, "b": Deny, "c": Allow})
}

// A directory without Cedar files adds no set and so denies nothing.
func TestEmptyDirInList(t *testing.T) {
	a := policyDir(t, map[string]string{"user.cedar": permitAll})
	wantVerdicts(t, loadDirs(t, a, t.TempDir(), filepath.Join(t.TempDir(), "missing")), map[string]string{"ls": Allow})
}
