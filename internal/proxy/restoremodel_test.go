package proxy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/GoldenDeals/aish/internal/session"
)

// A state saved before there were profiles has a model of the one endpoint
// aish had then: the shell stays on its profile and takes the model over.
func TestRestoreOldState(t *testing.T) {
	p, made := profiled(t)
	p.mu.Lock()
	p.restoreModel(session.Saved{Model: "old-model", Effort: "high"})
	p.mu.Unlock()
	if p.profile != "work" || p.model != "old-model" || p.effort != "high" {
		t.Errorf("restored %q %q %q", p.profile, p.model, p.effort)
	}
	if len(*made) != 0 {
		t.Errorf("providers made: %+v", *made)
	}
}

// A session switched to the top level goes back to it.
func TestRestoreTopLevel(t *testing.T) {
	p, made := profiled(t)
	p.mu.Lock()
	p.restoreModel(session.Saved{TopLevel: true, Model: "top-model"})
	p.mu.Unlock()
	if p.profile != "" || p.model != "top-model" {
		t.Errorf("restored %q %q", p.profile, p.model)
	}
	if len(*made) != 1 || (*made)[0].Profile != "" {
		t.Errorf("providers made: %+v", *made)
	}
}

// The state tells the top level from a state saved before profiles.
func TestModelStateTopLevel(t *testing.T) {
	p, _ := profiled(t)
	if err := p.sess.Save(); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"state.base", "state"} {
		if err := os.WriteFile(filepath.Join(p.run, f), []byte("\x00\x00\x00"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	saved := func(st session.Saved) session.Saved {
		t.Helper()
		p.mu.Lock()
		p.restoreModel(st)
		p.saveState("/srv")
		p.mu.Unlock()
		got, err := session.LoadState(p.sess.Dir(), p.sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if st := saved(session.Saved{TopLevel: true, Model: "top-model"}); st.Profile != "" || !st.TopLevel || st.Model != "top-model" {
		t.Errorf("the top level saved %+v", st)
	}
	if st := saved(session.Saved{Profile: "local", Model: "qwen3:8b"}); st.Profile != "local" || st.TopLevel || st.Model != "qwen3:8b" {
		t.Errorf("local saved %+v", st)
	}
}
