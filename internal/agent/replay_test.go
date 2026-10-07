package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/session"
	"github.com/GoldenDeals/aish/internal/tools"
)

func TestOwnRaw(t *testing.T) {
	raw := func(s string) json.RawMessage { return json.RawMessage(`"` + s + `"`) }
	es := []session.Entry{
		{Kind: session.KindUser, Text: "hi"},
		{Kind: session.KindAssistant, Text: "a", Raw: raw("work"), Provider: "p", Model: "m", Profile: "work"},
		{Kind: session.KindAssistant, Text: "b", Raw: raw("personal"), Provider: "p", Model: "m", Profile: "personal"},
		{Kind: session.KindAssistant, Text: "c", Raw: raw("old"), Provider: "p", Model: "m"},
	}
	kept := func(got []session.Entry) []string {
		var ps []string
		for _, e := range got {
			if len(e.Raw) > 0 {
				ps = append(ps, string(e.Raw))
			}
		}
		return ps
	}
	for _, c := range []struct {
		profile string
		want    string
	}{
		{"work", `"work"`},
		{"", `"old"`},
	} {
		got := ownRaw(es, c.profile)
		if ps := kept(got); len(ps) != 1 || ps[0] != c.want {
			t.Errorf("profile %q kept Raw of %v", c.profile, ps)
		}
		if &got[0] == &es[0] {
			t.Errorf("profile %q: dropped Raw in the caller's entries", c.profile)
		}
		if got[2].Provider != "p" || got[2].Model != "m" || got[2].Text != "b" {
			t.Errorf("profile %q: entry changed beyond Raw: %+v", c.profile, got[2])
		}
	}
	if ps := kept(es); len(ps) != 3 {
		t.Errorf("the entries themselves changed: Raw of %v", ps)
	}

	own := es[:2]
	if got := ownRaw(own, "work"); len(got) != len(own) || &got[0] != &own[0] {
		t.Errorf("nothing to drop, yet a copy")
	}
}

// A reply made under another profile goes to the model as text: its Raw is
// bound to the other profile's account.
func TestRequestDropsForeignRaw(t *testing.T) {
	a, _, _, _, _ := newAgent(t, &fakeProvider{})
	a.Cfg.Profile = "personal"
	es := []session.Entry{
		{Kind: session.KindUser, Text: "hi"},
		{Kind: session.KindAssistant, Text: "hello", Raw: json.RawMessage(`{"x":1}`), Provider: "fake", Model: "m", Profile: "work"},
		{Kind: session.KindUser, Text: "again"},
	}
	req := a.request(es)
	if len(req.Messages) != 3 {
		t.Fatalf("messages %+v", req.Messages)
	}
	if m := req.Messages[1]; len(m.Raw) != 0 || m.Provider != "fake" || m.Model != "m" || m.Text != "hello" {
		t.Errorf("reply of another profile %+v", m)
	}
	if len(es[1].Raw) == 0 {
		t.Errorf("Raw dropped from the entries themselves")
	}

	a.Cfg.Profile = "work"
	if m := a.request(es).Messages[1]; string(m.Raw) != `{"x":1}` {
		t.Errorf("reply of the same profile without Raw: %+v", m)
	}
}

// A reply records the profile it was made under.
func TestTurnRecordsProfile(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{{Text: "hi", Raw: json.RawMessage(`{"x":1}`)}}}
	a, j, _, _, cwd := newAgent(t, prov)
	a.Cfg.Profile = "work"
	if err := a.Start(context.Background(), "hello", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if got := kinds(j.es); got != "user assistant" {
		t.Fatalf("journal %s", got)
	}
	if e := j.es[1]; e.Profile != "work" || e.Provider != "fake" || e.Model != "m" {
		t.Errorf("assistant entry %+v", e)
	}
}
