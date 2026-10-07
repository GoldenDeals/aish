package proxy

import (
	"encoding/json"
	"testing"

	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/session"
)

func TestModelEffort(t *testing.T) {
	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(sess)
	p.prov, p.model, p.window, p.fixedWindow = &scripted{}, "a", 1000, true
	ctx := asUser(p)

	set := func(mp rpc.ModelParams) (rpc.Info, error) {
		b, _ := json.Marshal(mp)
		v, err := p.handle(ctx, rpc.MethodModel, b)
		if err != nil {
			return rpc.Info{}, err
		}
		return v.(rpc.Info), nil
	}
	if i, err := set(rpc.ModelParams{Model: "a", Effort: "xhigh"}); err != nil || i.Model != "a" || i.Effort != "xhigh" || i.Window != 1000 {
		t.Fatalf("effort only: %+v %v", i, err)
	}
	if _, err := set(rpc.ModelParams{Model: "a", Effort: "minimal"}); err == nil || p.effort != "xhigh" {
		t.Fatalf("a level the provider does not take: %v, effort %q", err, p.effort)
	}
	if text, _ := p.statusText(); text != "a · xhigh" {
		t.Errorf("status %q", text)
	}
	if i, _ := set(rpc.ModelParams{Model: "b"}); i.Model != "b" || i.Effort != "" {
		t.Fatalf("model with the default effort: %+v", i)
	}

	p.restoreModel(session.Saved{})
	if p.model != "b" || p.effort != "" {
		t.Errorf("a state without a model changed %q %q", p.model, p.effort)
	}
	p.restoreModel(session.Saved{Model: "c", Effort: "max"})
	if p.model != "c" || p.effort != "max" {
		t.Errorf("restored %q %q", p.model, p.effort)
	}
	p.restoreModel(session.Saved{Model: "c", Effort: "minimal"})
	if p.effort != "max" {
		t.Errorf("restored a level of another provider: %q", p.effort)
	}
	if i := p.info(); i.Model != "c" || i.Effort != "max" || i.Dir != sess.Dir() {
		t.Errorf("info %+v", i)
	}
}
