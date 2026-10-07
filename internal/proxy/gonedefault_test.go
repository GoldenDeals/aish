package proxy

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inebotov/aish/internal/config"
	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/rpc"
)

// A shell whose profile is gone, where the profile config.toml selects,
// by the profile key or by $AISH_PROFILE as the shell has it, is gone
// too, goes to the top level of config.toml: it has nowhere else to go,
// and every request would fail until `aish model`.
func TestGoneDefaultToRoot(t *testing.T) {
	t.Setenv("AISH_PROFILE", "")
	os.Unsetenv("AISH_PROFILE")
	noWork := strings.Replace(profilesTOML, "[profiles.work]", "[profiles.corp]", 1)
	for _, tc := range []struct {
		name, toml, why string
		env             []string
	}{
		{"profile key", strings.Replace(noWork, `profile = "work"`, `profile = "gone"`, 1),
			`[aish: config.toml: profile: no profile "gone" (profiles: corp, local)]`, nil},
		{"$AISH_PROFILE", strings.Replace(noWork, `profile = "work"`, `profile = "corp"`, 1),
			`[aish: config.toml: $AISH_PROFILE: no profile "gone" (profiles: corp, local)]`, []string{"AISH_PROFILE=gone"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := profiled(t)
			made := answering(p)
			rewrite(t, tc.toml)
			env := tc.env
			if env != nil {
				env = append(env, "AISH_CONFIG="+os.Getenv("AISH_CONFIG"))
			}
			ask(t, p, env)
			ask(t, p, env)
			p.mu.Lock()
			profile, model, effort := p.profile, p.model, p.effort
			p.mu.Unlock()
			if profile != "" || model != "top-model" || effort != "" {
				t.Errorf("the shell is on %q %q %q", profile, model, effort)
			}
			ms := made()
			if c := ms[len(ms)-1]; c.Profile != "" || c.Model != "top-model" || c.Provider != "" {
				t.Errorf("the agent's provider: %+v", c)
			}
			out := p.out.(*terminal).String()
			const line = "\x1b[2m[aish: profile work is not in config.toml: on root]\x1b[0m\r\n"
			if strings.Count(out, "not in config.toml") != 1 || strings.Count(out, line) != 1 {
				t.Errorf("not told once: %q", out)
			}
			if strings.Count(out, tc.why) != 1 {
				t.Errorf("not told why not the profile config.toml selects: %q", out)
			}
		})
	}
}

// The profile config.toml selects there but with no provider to be made
// of it is no place for the shell either.
func TestGoneDefaultUnusable(t *testing.T) {
	t.Setenv("AISH_PROFILE", "")
	os.Unsetenv("AISH_PROFILE")
	p, _ := profiled(t)
	p.newProvider = func(cfg config.Config) (llm.Provider, error) {
		if cfg.Provider == "nosuch" {
			return nil, errors.New(`unknown provider "nosuch"`)
		}
		return &scripted{replies: oks(5)}, nil
	}
	if _, err := call(t, p, rpc.MethodModel, rpc.ModelParams{Profile: "local", Model: "qwen3:8b"}); err != nil {
		t.Fatal(err)
	}
	toml := strings.Replace(profilesTOML, `provider = "anthropic"`, `provider = "nosuch"`, 1)
	rewrite(t, strings.Replace(toml, "[profiles.local]", "[profiles.lan]", 1))
	apply(t, p, nil)
	ask(t, p, nil)
	p.mu.Lock()
	profile, model := p.profile, p.model
	p.mu.Unlock()
	if profile != "" || model != "top-model" {
		t.Errorf("the shell is on %q %q", profile, model)
	}
	if out := p.out.(*terminal).String(); !strings.Contains(out, "[aish: profile local is not in config.toml: on root]") {
		t.Errorf("not told: %q", out)
	}
}

// $AISH_CONFIG exported or unset in the shell after aish started has the
// commands there read another file than the proxy: the user is told, once
// while it lasts.
func TestShellConfigPath(t *testing.T) {
	t.Setenv("AISH_PROFILE", "")
	os.Unsetenv("AISH_PROFILE")
	p, _ := profiled(t)
	p.newProvider = func(config.Config) (llm.Provider, error) { return &scripted{replies: oks(20)}, nil }
	reads := os.Getenv("AISH_CONFIG")
	other := filepath.Join(t.TempDir(), "other.toml")
	line := func(shell, reads string) string {
		return "\x1b[2m[aish: AISH_CONFIG in the shell is " + shell + ", aish reads " + reads + "; restart aish to switch]\x1b[0m\r\n"
	}
	out := func() string { return p.out.(*terminal).String() }

	ask(t, p, []string{"AISH_CONFIG=" + reads})
	if strings.Contains(out(), "AISH_CONFIG") {
		t.Errorf("told of the same file: %q", out())
	}
	ask(t, p, []string{"AISH_CONFIG=" + other})
	ask(t, p, []string{"AISH_CONFIG=" + other})
	if n := strings.Count(out(), line(other, reads)); n != 1 {
		t.Errorf("told %d times: %q", n, out())
	}
	ask(t, p, []string{"HOME=" + os.Getenv("HOME")})
	if n := strings.Count(out(), line("unset", reads)); n != 1 {
		t.Errorf("unset in the shell, told %d times: %q", n, out())
	}
	ask(t, p, []string{"AISH_CONFIG=" + reads})
	ask(t, p, []string{"AISH_CONFIG=" + other})
	if n := strings.Count(out(), line(other, reads)); n != 2 {
		t.Errorf("back again, told %d times", n)
	}

	// None in the proxy: it reads the default file, which the shell may
	// name, or not, to the same effect.
	def := filepath.Join(config.Dir(), "config.toml")
	if err := os.MkdirAll(filepath.Dir(def), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(def, []byte(profilesTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AISH_CONFIG", "")
	os.Unsetenv("AISH_CONFIG")
	before := len(out())
	ask(t, p, []string{"AISH_CONFIG=" + def})
	ask(t, p, []string{"HOME=" + os.Getenv("HOME")})
	if strings.Contains(out()[before:], "AISH_CONFIG") {
		t.Errorf("told of the default file: %q", out()[before:])
	}
	ask(t, p, []string{"AISH_CONFIG=" + other})
	if n := strings.Count(out(), line(other, def)); n != 1 {
		t.Errorf("told %d times of the default file: %q", n, out()[before:])
	}
}
