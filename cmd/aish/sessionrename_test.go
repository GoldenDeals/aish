package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/session"
)

// storedSession is a session in dir with a request, named name and titled title
// if given; open, it is left locked, as an aish holding it has it.
func storedSession(t *testing.T, dir, name, title string, open bool) *session.Session {
	t.Helper()
	s, err := session.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(session.Entry{Kind: session.KindUser, Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetName(name); err != nil {
		t.Fatal(err)
	}
	if title != "" {
		if err := s.SetTitle(title); err != nil {
			t.Fatal(err)
		}
	}
	if !open {
		s.Unlock()
	}
	return s
}

func nameOf(dir, id string) string {
	b, _ := os.ReadFile(filepath.Join(dir, id+".name"))
	return strings.TrimSpace(string(b))
}

// `aish session rename NAME` names this shell's session through the proxy,
// which holds it; with an id or a name first, another session, on disk.
func TestSessionRename(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.SessionsDir = dir
	left := storedSession(t, dir, "", "Fix nginx config", false)
	held := storedSession(t, dir, "elsewhere", "", true)
	got := fakeProxy(t, map[string]any{
		rpc.MethodInfo:   rpc.Info{SessionID: "cur", Dir: dir},
		rpc.MethodRename: rpc.Info{SessionID: "cur", Name: "work"},
	})

	code, stdout, stderr := captured(t, func() int { return sessionRenameCmd(cfg, []string{"work"}) })
	if code != 0 || string(got[rpc.MethodRename]) != `{"name":"work"}` || stdout != "session cur is work now\n" {
		t.Errorf("this shell's: exit %d, %s, %q %q", code, got[rpc.MethodRename], stdout, stderr)
	}
	delete(got, rpc.MethodRename)

	// Another one, found by the model's name.
	if code, _, stderr := captured(t, func() int { return sessionRenameCmd(cfg, []string{"fix nginx", "nginx"}) }); code != 0 || nameOf(dir, left.ID) != "nginx" {
		t.Errorf("another: exit %d, %q, named %q", code, stderr, nameOf(dir, left.ID))
	}
	if code, _, _ := captured(t, func() int { return sessionRenameCmd(cfg, []string{left.ID, ""}) }); code != 0 || nameOf(dir, left.ID) != "" {
		t.Errorf("name taken away: exit %d, named %q", code, nameOf(dir, left.ID))
	}
	// Open in another aish: it names its session itself.
	if code, _, stderr := captured(t, func() int { return sessionRenameCmd(cfg, []string{"elsewhere", "x"}) }); code == 0 || !strings.Contains(stderr, "open in another aish") || nameOf(dir, held.ID) != "elsewhere" {
		t.Errorf("open elsewhere: exit %d, %q", code, stderr)
	}
	for _, args := range [][]string{nil, {"a", "b", "c"}, {"-x"}, {"nope", "x"}} {
		if code, _, _ := captured(t, func() int { return sessionRenameCmd(cfg, args) }); code == 0 {
			t.Errorf("%q: exit 0", args)
		}
	}
	if len(got) > 1 {
		t.Errorf("rpc beyond info: %v", got)
	}

	// Outside aish there is no session of this shell.
	t.Setenv("AISH_SOCK", "")
	if code, _, _ := captured(t, func() int { return sessionRenameCmd(cfg, []string{"x"}) }); code == 0 {
		t.Error("named the session of no shell")
	}
	if code, _, _ := captured(t, func() int { return sessionRenameCmd(cfg, []string{left.ID, "outside"}) }); code != 0 || nameOf(dir, left.ID) != "outside" {
		t.Errorf("outside: exit %d, named %q", code, nameOf(dir, left.ID))
	}
}
