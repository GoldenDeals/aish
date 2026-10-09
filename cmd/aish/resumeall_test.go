package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/session"
)

func TestResumeArgs(t *testing.T) {
	for _, c := range []struct {
		args []string
		all  bool
		q    string
	}{
		{nil, false, ""},
		{[]string{"--all"}, true, ""},
		{[]string{"-a", "deploy"}, true, "deploy"},
		{[]string{"deploy", "--all"}, true, "deploy"},
		{[]string{"deploy"}, false, "deploy"},
	} {
		all, q, err := resumeArgs(c.args)
		if err != nil || all != c.all || strings.Join(q, " ") != c.q {
			t.Errorf("%q: %v %q %v", c.args, all, q, err)
		}
	}
	for _, args := range [][]string{{"--bogus"}, {"a", "b"}, {"-x", "a"}} {
		if _, _, err := resumeArgs(args); err == nil {
			t.Errorf("%q: no error", args)
		}
	}
}

// `aish resume` offers the sessions the user named, by the user's names;
// --all every one, by the model's name if the user gave none. An id finds
// any.
func TestResumeAll(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.SessionsDir = dir
	named := storedSession(t, dir, "deploy", "Deploy the app", false)
	titled := storedSession(t, dir, "", "Fix nginx config", false)
	bare := storedSession(t, dir, "", "", false)
	list, err := session.List(dir)
	if err != nil {
		t.Fatal(err)
	}

	ids := func(list []session.Info) string {
		var s []string
		for _, i := range list {
			s = append(s, i.ID)
		}
		return strings.Join(s, " ")
	}
	if shown, err := toChoose(list, false); err != nil || ids(shown) != named.ID {
		t.Errorf("without --all: %s %v", ids(shown), err)
	}
	if shown, err := toChoose(list, true); err != nil || len(shown) != 3 {
		t.Errorf("with --all: %s %v", ids(shown), err)
	}
	if _, err := toChoose(session.Named(list)[:0], false); err == nil || !strings.Contains(err.Error(), "no sessions yet") {
		t.Errorf("none at all: %v", err)
	}
	var unnamed []session.Info
	for _, i := range list {
		if i.Name == "" {
			unnamed = append(unnamed, i)
		}
	}
	if _, err := toChoose(unnamed, false); err == nil || !strings.Contains(err.Error(), "--all") {
		t.Errorf("none named: %v", err)
	}

	p := &picker{dir: dir, list: list, all: true, w: 100, h: 24}
	if s := p.render(); !strings.Contains(s, "all sessions") || !strings.Contains(s, "deploy") ||
		!strings.Contains(s, "Fix nginx config") || !strings.Contains(s, bare.ID) || strings.Contains(s, "Deploy the app") {
		t.Errorf("picker --all %q", s)
	}

	got := fakeProxy(t, map[string]any{
		rpc.MethodInfo:   rpc.Info{SessionID: "cur", Dir: dir},
		rpc.MethodResume: rpc.Info{},
	})
	resumed := func(args ...string) string {
		t.Helper()
		delete(got, rpc.MethodResume)
		captured(t, func() int { return resumeCmd(nil, cfg, args) })
		var rp rpc.ResumeParams
		_ = json.Unmarshal(got[rpc.MethodResume], &rp)
		return rp.ID
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"Fix nginx"}, ""},
		{[]string{"--all", "Fix nginx"}, titled.ID},
		{[]string{"dep"}, named.ID},
		{[]string{bare.ID}, bare.ID},
	} {
		if id := resumed(c.args...); id != c.want {
			t.Errorf("%q: resumed %q, want %q", c.args, id, c.want)
		}
	}
}

// Renamed in the picker without --all, a session that lost its name leaves
// the list, and the picker closes with the last one.
func TestPickerRename(t *testing.T) {
	dir := t.TempDir()
	a := storedSession(t, dir, "a", "", false)
	b := storedSession(t, dir, "b", "", false)
	var renamed []string
	rename := func(id, name string) error {
		renamed = append(renamed, id+"="+name)
		return session.Rename(dir, id, name)
	}
	list, _ := session.List(dir)
	p := &picker{dir: dir, list: session.Named(list), rename: rename, w: 80, h: 24}
	for n, i := range p.list {
		if i.ID == a.ID {
			p.sel = n
		}
	}
	p.key("r")
	for _, k := range []string{"\x15", "\r"} {
		if done, _ := p.key(k); done {
			t.Fatal("closed with a session left")
		}
	}
	if len(p.list) != 1 || p.list[0].Name != "b" || !strings.Contains(p.render(), "aish resume --all") {
		t.Errorf("after the name of a went: %+v", p.list)
	}
	p.key("r")
	p.key("\x15")
	if done, _ := p.key("\r"); !done {
		t.Error("open with no session to choose")
	}
	if strings.Join(renamed, " ") != a.ID+"= "+b.ID+"=" {
		t.Errorf("renamed %q", renamed)
	}
}
