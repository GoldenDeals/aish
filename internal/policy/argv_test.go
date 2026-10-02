package policy

import (
	"reflect"
	"testing"
)

func TestAnalyze(t *testing.T) {
	const home, cwd = "/home/me", "/home/me/src"
	for _, c := range []struct {
		argv     []string
		flags    []string
		operands []string
		paths    []string
	}{
		{[]string{"rm", "-rf", "~/"}, []string{"r", "f"}, []string{"~/"}, []string{home}},
		{[]string{"rm", "-rf", "$HOME"}, []string{"r", "f"}, []string{"$HOME"}, []string{home}},
		{[]string{"rm", "-rf", "${HOME}/"}, []string{"r", "f"}, []string{"${HOME}/"}, []string{home}},
		{[]string{"rm", "-rf", "/home/me/../me/"}, []string{"r", "f"}, []string{"/home/me/../me/"}, []string{home}},
		{[]string{"rm", "-rf", "/"}, []string{"r", "f"}, []string{"/"}, []string{"/"}},
		{[]string{"rm", "-rf", "build"}, []string{"r", "f"}, []string{"build"}, nil},
		{[]string{"rm", "-r", "./build", "../x"}, []string{"r"}, []string{"./build", "../x"}, []string{cwd + "/build", home + "/x"}},
		{[]string{"git", "push", "--force-with-lease", "origin"}, []string{"force-with-lease"}, []string{"push", "origin"}, nil},
		{[]string{"git", "clone", "--depth=1", "u", "a/b"}, []string{"depth"}, []string{"clone", "u", "a/b"}, []string{cwd + "/a/b"}},
		{[]string{"pacman", "-Syu"}, []string{"S", "y", "u"}, nil, nil},
		{[]string{"apt", "install", "ripgrep"}, nil, []string{"install", "ripgrep"}, nil},
		{[]string{"rm", "--", "-rf", "-"}, nil, []string{"-rf", "-"}, nil},
		{[]string{"cd", "-"}, nil, []string{"-"}, nil},
		{[]string{"cat", "$DIR/x"}, nil, []string{"$DIR/x"}, nil},
	} {
		got := Analyze(c.argv, cwd, home)
		if !reflect.DeepEqual(got.Flags, c.flags) || !reflect.DeepEqual(got.Operands, c.operands) || !reflect.DeepEqual(got.Paths, c.paths) {
			t.Errorf("%q: flags %q operands %q paths %q, want %q %q %q", c.argv, got.Flags, got.Operands, got.Paths, c.flags, c.operands, c.paths)
		}
	}
	got := Analyze([]string{"/usr/bin/sudo", "-u", "root", "ls"}, cwd, home)
	if got.Program != "sudo" || got.Text != "/usr/bin/sudo -u root ls" || !reflect.DeepEqual(got.Args, []string{"-u", "root", "ls"}) {
		t.Errorf("%+v", got)
	}
	if got := Analyze(nil, cwd, home); got.Program != "" || got.Text != "" {
		t.Errorf("%+v", got)
	}
}
