package proxy

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/session"
)

const profilesTOML = `
model = "top-model"
profile = "work"

[profiles.work]
provider = "anthropic"
model = "claude-opus-5"

[profiles.local]
provider = "openai"
base_url = "http://localhost:11434"
model = "qwen3:32b"
effort = "low"
context_window = 32000
`

// A proxy started on profile "work", whose providers are made by the
// registry's stand-in, which records the configs it got.
func profiled(t *testing.T) (*Proxy, *[]config.Config) {
	t.Helper()
	p, _, _ := hosted(t, &scripted{})
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(profilesTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AISH_CONFIG", path)
	var mu sync.Mutex
	var made []config.Config
	p.newProvider = func(cfg config.Config) (llm.Provider, error) {
		mu.Lock()
		defer mu.Unlock()
		made = append(made, cfg)
		return &scripted{}, nil
	}
	p.prov, p.profile, p.defProfile = &scripted{}, "work", "work"
	p.model, p.window = "claude-opus-5", 200000
	return p, &made
}

func TestSwitchProfile(t *testing.T) {
	p, made := profiled(t)
	set := func(mp rpc.ModelParams) (rpc.Info, error) {
		v, err := call(t, p, rpc.MethodModel, mp)
		if err != nil {
			return rpc.Info{}, err
		}
		return v.(rpc.Info), nil
	}
	if text, _ := p.statusText(); text != "claude-opus-5" {
		t.Errorf("the profile config.toml selects is not named: %q", text)
	}

	i, err := set(rpc.ModelParams{Profile: "local", Model: "qwen3:8b", Effort: "high"})
	if err != nil || i.Profile != "local" || i.Model != "qwen3:8b" || i.Effort != "high" || i.Window != 32000 {
		t.Fatalf("to local: %+v %v", i, err)
	}
	// A provider of the profile, from the registry, for the models list.
	if len(*made) != 1 || (*made)[0].Provider != "openai" || (*made)[0].BaseURL != "http://localhost:11434" || (*made)[0].Effort != "" {
		t.Errorf("providers made: %+v", *made)
	}
	if text, _ := p.statusText(); text != "local · qwen3:8b · high" {
		t.Errorf("status %q", text)
	}

	// Back: the window of local's context_window is not work's.
	if i, err = set(rpc.ModelParams{Profile: "work", Model: "claude-opus-5", Window: 1000}); err != nil || i.Profile != "work" || i.Window != 1000 || p.fixedWindow {
		t.Fatalf("back to work: %+v %v, fixed %v", i, err, p.fixedWindow)
	}
	if text, _ := p.statusText(); text != "claude-opus-5" {
		t.Errorf("status %q", text)
	}
	// The same profile makes no provider.
	if _, err := set(rpc.ModelParams{Profile: "work", Model: "claude-sonnet-5", Window: 1000}); err != nil || len(*made) != 2 {
		t.Errorf("a model of the same profile: %v, %d providers", err, len(*made))
	}

	for _, mp := range []rpc.ModelParams{
		{Profile: "nope", Model: "x"},
		{Profile: "local", Model: "qwen3:8b", Effort: "minimal"},
	} {
		if _, err := set(mp); err == nil || p.profile != "work" || p.model != "claude-sonnet-5" {
			t.Errorf("%+v: %v; now %s %s", mp, err, p.profile, p.model)
		}
	}
	if _, err := set(rpc.ModelParams{Profile: "nope", Model: "x"}); err == nil || !strings.Contains(err.Error(), `no profile "nope"`) {
		t.Errorf("an unknown profile: %v", err)
	}
}

// After aish resume the profile of the session is back, its model with it.
func TestRestoreProfile(t *testing.T) {
	p, _ := profiled(t)
	p.restoreModel(session.Saved{Profile: "local", Model: "qwen3:8b", Effort: "high"})
	if p.profile != "local" || p.model != "qwen3:8b" || p.effort != "high" || p.window != 32000 {
		t.Errorf("restored %q %q %q %d", p.profile, p.model, p.effort, p.window)
	}
	p.restoreModel(session.Saved{Profile: "gone", Model: "x", Effort: "max"})
	if p.profile != "local" || p.model != "qwen3:8b" || p.effort != "high" {
		t.Errorf("a profile config.toml has no more: %q %q %q", p.profile, p.model, p.effort)
	}
	// The top level, as a state saves it now.
	p.restoreModel(session.Saved{TopLevel: true, Model: "top-model", Effort: "max"})
	if p.profile != "" || p.model != "top-model" || p.effort != "max" || p.fixedWindow {
		t.Errorf("the top level: %q %q %q", p.profile, p.model, p.effort)
	}

	// And the state keeps it.
	if err := p.sess.Save(); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"state.base", "state"} {
		if err := os.WriteFile(filepath.Join(p.run, f), []byte("\x00\x00\x00"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	p.mu.Lock()
	p.restoreModel(session.Saved{Profile: "local", Model: "qwen3:8b", Effort: "low"})
	p.saveState("/srv")
	p.mu.Unlock()
	st, err := session.LoadState(p.sess.Dir(), p.sess.ID)
	if err != nil || st.Profile != "local" || st.Model != "qwen3:8b" || st.Effort != "low" {
		t.Errorf("saved %+v %v", st, err)
	}
}

// A request goes to the endpoint of the shell's profile.
func TestRequestOfProfile(t *testing.T) {
	p, made := profiled(t)
	if _, err := call(t, p, rpc.MethodModel, rpc.ModelParams{Profile: "local", Model: "qwen3:8b"}); err != nil {
		t.Fatal(err)
	}
	p.marker(Marker{Kind: "ask-start"})
	// No reply is scripted: only the provider the agent got matters.
	_, _ = call(t, p, rpc.MethodAgentStart, rpc.AgentParams{Text: "hi", Cwd: filepath.Join(os.Getenv("HOME"), "work")})
	if len(*made) != 2 {
		t.Fatalf("providers made: %+v", *made)
	}
	if c := (*made)[1]; c.Profile != "local" || c.Provider != "openai" || c.BaseURL != "http://localhost:11434" ||
		c.Model != "qwen3:8b" || c.ContextWindow != 32000 {
		t.Errorf("the agent's provider: %+v", c)
	}
}
