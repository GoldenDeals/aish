package session

import (
	"os"
	"path/filepath"
	"testing"
)

// List tells the user's name of a session from the model's; Find goes by
// the user's, FindAll by the model's too.
func TestUserAndModelNames(t *testing.T) {
	dir := t.TempDir()
	made := func(name, title string) *Session {
		t.Helper()
		s, err := New(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Append(Entry{Kind: KindUser, Text: "hi"}); err != nil {
			t.Fatal(err)
		}
		if name != "" {
			if err := s.SetName(name); err != nil {
				t.Fatal(err)
			}
		}
		if title != "" {
			if err := s.SetTitle(title); err != nil {
				t.Fatal(err)
			}
		}
		s.Unlock()
		return s
	}
	named := made("deploy", "Deploy the app")
	titled := made("", "Fix nginx config")
	bare := made("", "")
	twin := made("", "Fix nginx config")

	list, err := List(dir)
	if err != nil || len(list) != 4 {
		t.Fatalf("%v %+v", err, list)
	}
	byID := map[string]Info{}
	for _, i := range list {
		byID[i.ID] = i
	}
	for _, c := range []struct {
		s                     *Session
		name, auto, wantTitle string
	}{
		{named, "deploy", "Deploy the app", "deploy"},
		{titled, "", "Fix nginx config", "Fix nginx config"},
		{bare, "", "", bare.ID},
	} {
		i := byID[c.s.ID]
		if i.Name != c.name || i.AutoName != c.auto || i.Title() != c.wantTitle {
			t.Errorf("%s: name %q, auto %q, title %q", c.s.ID, i.Name, i.AutoName, i.Title())
		}
	}
	if n := Named(list); len(n) != 1 || n[0].ID != named.ID {
		t.Errorf("named %+v", n)
	}

	for _, c := range []struct {
		find func([]Info, string) (Info, error)
		q    string
		want string // "" for none
	}{
		{Find, "deploy", named.ID},
		{Find, "Fix nginx config", ""},
		{Find, bare.ID, bare.ID},
		{FindAll, "deploy", named.ID},
		{FindAll, "Deploy the", ""},       // the model's name of a session the user named is not its name
		{FindAll, "Fix nginx config", ""}, // two have it
		{FindAll, bare.ID, bare.ID},
	} {
		i, err := c.find(list, c.q)
		if c.want == "" && err == nil || c.want != "" && (err != nil || i.ID != c.want) {
			t.Errorf("find %q: %s %v, want %q", c.q, i.ID, err, c.want)
		}
	}
	if err := Rename(dir, twin.ID, "nginx"); err != nil {
		t.Fatal(err)
	}
	list, _ = List(dir)
	if i, err := FindAll(list, "fix"); err != nil || i.ID != titled.ID {
		t.Errorf("find fix: %s %v", i.ID, err)
	}
	if i, err := Find(list, "ngi"); err != nil || i.ID != twin.ID {
		t.Errorf("find ngi: %s %v", i.ID, err)
	}
}

// Rename names a session nobody holds, under its lock; SetTitle locks one
// the shell has left.
func TestRenameLocks(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	if err := s.Append(Entry{Kind: KindUser, Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if err := Rename(dir, s.ID, "x"); err == nil {
		t.Error("renamed a session an aish holds")
	}
	if err := Rename(dir, "20200101-000000-1", "x"); err == nil {
		t.Error("renamed a session that is not there")
	}
	if _, err := os.Stat(filepath.Join(dir, "20200101-000000-1.lock")); !os.IsNotExist(err) {
		t.Errorf("a lock left behind: %v", err)
	}
	s.Unlock()
	if err := Rename(dir, s.ID, "x"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTitle("Say hi"); err != nil {
		t.Fatal(err)
	}
	if isOpen(dir, s.ID) {
		t.Error("SetTitle left the session locked")
	}
	l, err := lock(dir, s.ID) // another aish took it
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetTitle("Other"); err == nil {
		t.Error("titled a session another aish holds")
	}
	unlock(l)
	if err := Rename(dir, s.ID, ""); err != nil {
		t.Fatal(err)
	}
	list, _ := List(dir)
	if len(list) != 1 || list[0].Name != "" || list[0].AutoName != "Say hi" {
		t.Errorf("%+v", list)
	}
}
