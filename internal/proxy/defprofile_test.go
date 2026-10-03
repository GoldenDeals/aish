package proxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inebotov/aish/internal/rpc"
)

// The profile config.toml selects for the shell goes by $AISH_PROFILE as
// the shell has it, exported or unset there after aish started, not as
// the proxy's own environment has it.
func TestDefProfileFromShell(t *testing.T) {
	for _, tc := range []struct {
		name, proxy string
		shell       []string
		def         string
	}{
		{"exported in the shell", "", []string{"AISH_PROFILE=work"}, "work"},
		{"unset in the shell", "work", []string{"HOME=/nowhere"}, "local"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AISH_PROFILE", tc.proxy)
			if tc.proxy == "" {
				os.Unsetenv("AISH_PROFILE")
			}
			p, _ := profiled(t)
			answering(p)
			rewrite(t, strings.Replace(profilesTOML, `profile = "work"`, `profile = "local"`, 1))
			ask(t, p, tc.shell)
			p.mu.Lock()
			def, profile := p.defProfile, p.profile
			p.mu.Unlock()
			if def != tc.def || profile != "work" {
				t.Errorf("config.toml selects %q, the shell is on %q", def, profile)
			}
		})
	}
}

// A profile config.toml selects but has not leaves the shell on its own,
// and the user is told why the status may name it: once while it lasts,
// again when it comes back.
func TestDefProfileGoneTold(t *testing.T) {
	t.Setenv("AISH_PROFILE", "")
	os.Unsetenv("AISH_PROFILE")
	p, _ := profiled(t)
	answering(p)
	gone := strings.Replace(profilesTOML, `profile = "work"`, `profile = "gone"`, 1)
	const line = "\x1b[2m[aish: config.toml: profile: no profile \"gone\" (profiles: local, work)]\x1b[0m\r\n"
	told := func() int { return strings.Count(p.out.(*terminal).String(), line) }

	rewrite(t, gone)
	ask(t, p, nil)
	ask(t, p, nil)
	if n := told(); n != 1 {
		t.Errorf("told %d times: %q", n, p.out.(*terminal).String())
	}
	p.mu.Lock()
	def, profile := p.defProfile, p.profile
	p.mu.Unlock()
	if def != "work" || profile != "work" {
		t.Errorf("config.toml selects %q, the shell is on %q", def, profile)
	}

	rewrite(t, profilesTOML)
	ask(t, p, nil)
	rewrite(t, gone)
	ask(t, p, nil)
	if n := told(); n != 2 {
		t.Errorf("back again, told %d times", n)
	}

	// A config.toml the shell's profile fails on too is the request's
	// error, not a line besides it.
	rewrite(t, "model = [\n")
	p.marker(Marker{Kind: "ask-start"})
	before := p.out.(*terminal).String()
	ap := rpc.AgentParams{Text: "hi", Cwd: filepath.Join(os.Getenv("HOME"), "work")}
	if _, err := call(t, p, rpc.MethodAgentStart, ap); err == nil {
		t.Fatal("a broken config.toml did not fail the request")
	}
	if after := p.out.(*terminal).String(); strings.Contains(after[len(before):], "[aish: config.toml") {
		t.Errorf("told besides the error: %q", after[len(before):])
	}
}
