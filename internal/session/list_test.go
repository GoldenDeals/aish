package session

import "testing"

func TestListProfile(t *testing.T) {
	dir := t.TempDir()
	states := map[string]Saved{
		"local": {Profile: "local", Model: "qwen3:8b"},
		"root":  {TopLevel: true, Model: "claude-opus-5"},
		"old":   {Model: "claude-opus-5"},
	}
	ids := map[string]string{}
	for name, st := range states {
		s, err := New(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Save(); err != nil {
			t.Fatal(err)
		}
		s.Unlock()
		if err := SaveState(dir, s.ID, st); err != nil {
			t.Fatal(err)
		}
		ids[s.ID] = name
	}
	list, err := List(dir)
	if err != nil || len(list) != len(states) {
		t.Fatalf("%v %+v", err, list)
	}
	for _, i := range list {
		want := states[ids[i.ID]]
		if i.Profile != want.Profile || i.TopLevel != want.TopLevel || i.Model != want.Model {
			t.Errorf("%s: %+v, want %+v", ids[i.ID], i, want)
		}
	}
}
