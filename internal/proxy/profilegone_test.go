package proxy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inebotov/aish/internal/config"
	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/rpc"
)

// answering has the proxy of profiled make providers that answer every
// request, recording the configs they are made with.
func answering(p *Proxy) func() []config.Config {
	var mu sync.Mutex
	var made []config.Config
	p.newProvider = func(cfg config.Config) (llm.Provider, error) {
		mu.Lock()
		defer mu.Unlock()
		made = append(made, cfg)
		return &scripted{replies: oks(5)}, nil
	}
	return func() []config.Config {
		mu.Lock()
		defer mu.Unlock()
		return append([]config.Config(nil), made...)
	}
}

func oks(n int) []*llm.Response {
	var rs []*llm.Response
	for range n {
		rs = append(rs, &llm.Response{Text: "ok"})
	}
	return rs
}

// rewrite replaces the config.toml of profiled, as the user edits it with
// the shell running.
func rewrite(t *testing.T, toml string) {
	t.Helper()
	if err := os.WriteFile(os.Getenv("AISH_CONFIG"), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
}

// ask is a request of the user, which must get through.
func ask(t *testing.T, p *Proxy, env []string) {
	t.Helper()
	p.marker(Marker{Kind: "ask-start"})
	ap := rpc.AgentParams{Text: "hi", Cwd: filepath.Join(os.Getenv("HOME"), "work"), Env: env}
	if _, err := call(t, p, rpc.MethodAgentStart, ap); err != nil {
		t.Fatal(err)
	}
}

// A profile renamed or removed in config.toml would fail every request:
// the shell goes to the one config.toml selects, and is told so.
func TestGoneProfile(t *testing.T) {
	t.Setenv("AISH_PROFILE", "")
	withoutLocal, _, _ := strings.Cut(profilesTOML, "[profiles.local]")
	for _, tc := range []struct {
		name, toml, profile, model, line string
	}{
		{"renamed", withoutLocal, "work", "claude-opus-5", "[aish: profile local is not in config.toml: on work]"},
		{"none left", "model = \"top-model\"\n", "", "top-model", "[aish: profile local is not in config.toml: on root]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := profiled(t)
			made := answering(p)
			if _, err := call(t, p, rpc.MethodModel, rpc.ModelParams{Profile: "local", Model: "qwen3:8b", Effort: "high"}); err != nil {
				t.Fatal(err)
			}
			rewrite(t, tc.toml)
			ask(t, p, nil)
			p.mu.Lock()
			profile, model, effort := p.profile, p.model, p.effort
			p.mu.Unlock()
			if profile != tc.profile || model != tc.model || effort != "" {
				t.Errorf("the shell is on %q %q %q", profile, model, effort)
			}
			ms := made()
			if c := ms[len(ms)-1]; c.Profile != tc.profile || c.Model != tc.model || c.Provider == "openai" {
				t.Errorf("the agent's provider: %+v", c)
			}
			out := p.out.(*terminal).String()
			if !strings.Contains(out, "not in config.toml") || strings.Count(out, tc.line) != 1 {
				t.Errorf("not told: %q", out)
			}
			ask(t, p, nil)
			if out := p.out.(*terminal).String(); strings.Count(out, "not in config.toml") != 1 {
				t.Errorf("told again: %q", out)
			}
		})
	}
}

// A profile config.toml selects but has not, by $AISH_PROFILE or by the
// profile key, does not fail the request of a shell on another one; the
// shell's own gone too, the request tells why it fails.
func TestSelectedProfileGone(t *testing.T) {
	renamed := strings.Replace(profilesTOML, "[profiles.work]", "[profiles.corp]", 1)
	for _, tc := range []struct{ name, env, toml string }{
		{"$AISH_PROFILE", "work", strings.Replace(renamed, `profile = "work"`, `profile = "corp"`, 1)},
		{"profile key", "", renamed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := profiled(t)
			answering(p)
			if _, err := call(t, p, rpc.MethodModel, rpc.ModelParams{Profile: "local", Model: "qwen3:8b"}); err != nil {
				t.Fatal(err)
			}
			t.Setenv("AISH_PROFILE", tc.env)
			rewrite(t, tc.toml)
			ask(t, p, nil)
			p.mu.Lock()
			profile, def := p.profile, p.defProfile
			p.mu.Unlock()
			if profile != "local" || def != "work" {
				t.Errorf("the shell is on %q, config.toml selects %q", profile, def)
			}

			rewrite(t, strings.Replace(tc.toml, "[profiles.local]", "[profiles.lan]", 1))
			p.marker(Marker{Kind: "ask-start"})
			ap := rpc.AgentParams{Text: "hi", Cwd: filepath.Join(os.Getenv("HOME"), "work")}
			if _, err := call(t, p, rpc.MethodAgentStart, ap); err == nil || !strings.Contains(err.Error(), `no profile "local"`) {
				t.Errorf("the shell's profile gone too: %v", err)
			}
		})
	}
}

// The status names the shell's profile when it is not the one config.toml
// selects, as config.toml is now, not as it was when aish started.
func TestDefProfileFollowsConfig(t *testing.T) {
	t.Setenv("AISH_PROFILE", "")
	p, _ := profiled(t)
	answering(p)
	rewrite(t, strings.Replace(profilesTOML, `profile = "work"`, `profile = "local"`, 1))
	ask(t, p, nil)
	p.mu.Lock()
	def, profile := p.defProfile, p.profile
	text, _ := p.statusText()
	p.mu.Unlock()
	if def != "local" || profile != "work" {
		t.Errorf("config.toml selects %q, the shell is on %q", def, profile)
	}
	if !strings.Contains(text, "work · claude-opus-5") {
		t.Errorf("status %q", text)
	}
}

const corpTOML = `
profile = "corp"

[profiles.corp]
provider = "anthropic"
api_key_env = "CORP_KEY"
model = "claude-opus-5"
`

// windowed lists the models only for the key it was made with, as the API
// does; each listing is sent to calls.
type windowed struct {
	scripted
	key   string
	calls chan<- string
}

func (w *windowed) Models(context.Context) ([]llm.ModelInfo, error) {
	w.calls <- w.key
	if w.key != "secret" {
		return nil, errors.New("401 Unauthorized")
	}
	return []llm.ModelInfo{{ID: "claude-opus-5", Window: 1_000_000}, {ID: "no-window"}}, nil
}

// The key exported in the shell alone, not in the proxy's environment,
// still finds out the window: the agent's provider, which has that key,
// is asked.
func TestWindowWithShellKey(t *testing.T) {
	t.Setenv("AISH_PROFILE", "")
	p, _ := profiled(t)
	rewrite(t, corpTOML)
	t.Setenv("CORP_KEY", "")
	os.Unsetenv("CORP_KEY")
	calls := make(chan string, 16)
	p.newProvider = func(cfg config.Config) (llm.Provider, error) {
		return &windowed{scripted: scripted{replies: oks(5)}, key: cfg.APIKey, calls: calls}, nil
	}
	set := func(model string) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.profile, p.defProfile, p.model, p.window, p.fixedWindow = "corp", "corp", model, 0, false
	}
	env := []string{"CORP_KEY=secret"}
	listed := func() {
		t.Helper()
		select {
		case key := <-calls:
			if key != "secret" {
				t.Errorf("listed with key %q", key)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the models were not listed")
		}
	}
	notListed := func() {
		t.Helper()
		select {
		case key := <-calls:
			t.Errorf("listed again, with key %q", key)
		case <-time.After(100 * time.Millisecond):
		}
	}
	window := func(want int) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
			p.mu.Lock()
			w := p.window
			p.mu.Unlock()
			if w == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("window %d, want %d", w, want)
			}
		}
	}

	set("claude-opus-5")
	ask(t, p, env)
	listed()
	window(1_000_000)
	ask(t, p, env)
	notListed()

	// The window lost on the same model, as `aish resume` of a session on
	// it leaves it: asked anew.
	set("claude-opus-5")
	ask(t, p, env)
	listed()
	window(1_000_000)

	// A model the list reports no window for is asked about once.
	set("no-window")
	ask(t, p, env)
	listed()
	ask(t, p, env)
	notListed()
	window(0)
}
