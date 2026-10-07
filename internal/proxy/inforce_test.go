package proxy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/mcp"
	"github.com/GoldenDeals/aish/internal/policy"
	"github.com/GoldenDeals/aish/internal/rpc"
)

// inForce is rpc config of the shell in its work directory.
func inForce(t *testing.T, p *Proxy, cp rpc.ConfigParams) rpc.Config {
	t.Helper()
	cp.Cwd = filepath.Join(os.Getenv("HOME"), "work")
	v, err := call(t, p, rpc.MethodConfig, cp)
	if err != nil {
		t.Fatal(err)
	}
	return v.(rpc.Config)
}

// asked is what `aish policy bash COMMAND` gets from the proxy.
func asked(t *testing.T, p *Proxy, command string) string {
	t.Helper()
	pp := rpc.PolicyParams{Cwd: filepath.Join(os.Getenv("HOME"), "work"), Tool: "bash",
		Args: map[string]any{"command": command}, Line: command, HandOff: true}
	v, err := call(t, p, rpc.MethodPolicy, pp)
	if err != nil {
		t.Fatal(err)
	}
	return v.(policy.Decision).Action
}

// aish policy and aish status inside aish go by the config in force: an
// edit of [policy] answers once applied, and is named until then.
func TestInForcePolicy(t *testing.T) {
	p := configured(t, "[policy]\ndeny = [\"rm *\"]\n")
	if asked(t, p, "rm x") != policy.Deny || asked(t, p, "ls x") != policy.Allow {
		t.Fatal("as started")
	}
	res := inForce(t, p, rpc.ConfigParams{Policies: true})
	if res.Global != 1 || res.PolicyErr != "" || len(res.Changed) > 0 || !slices.Equal(res.Config.Policy.Deny, []string{"rm *"}) {
		t.Errorf("as started: %+v", res)
	}

	rewrite(t, "[policy]\ndeny = [\"ls *\"]\n")
	if asked(t, p, "rm x") != policy.Deny || asked(t, p, "ls x") != policy.Allow {
		t.Error("an edit not applied answers")
	}
	res = inForce(t, p, rpc.ConfigParams{Policies: true})
	if !slices.Equal(res.Changed, []string{os.Getenv("AISH_CONFIG")}) || !slices.Equal(res.Config.Policy.Deny, []string{"rm *"}) {
		t.Errorf("edited: changed %q, deny %q", res.Changed, res.Config.Policy.Deny)
	}

	apply(t, p, nil)
	if asked(t, p, "rm x") != policy.Allow || asked(t, p, "ls x") != policy.Deny {
		t.Error("the edit applied does not answer")
	}
	if res := inForce(t, p, rpc.ConfigParams{}); len(res.Changed) > 0 || !slices.Equal(res.Config.Policy.Deny, []string{"ls *"}) {
		t.Errorf("applied: changed %q, deny %q", res.Changed, res.Config.Policy.Deny)
	}
}

// The config a command gets carries no key and no proxy of the requests,
// of any profile; the profiles are there by name.
func TestInForceSecrets(t *testing.T) {
	p := configured(t, `api_key = "sk-top-xyzzy"
https_proxy = "http://user:pw-xyzzy@proxy:3128"
profile = "work"

[profiles.work]
api_key = "sk-work-xyzzy"
all_proxy = "socks5://user:pw-xyzzy@proxy:1080"
model = "w"
`)
	work := "work"
	for _, cp := range []rpc.ConfigParams{{}, {Profile: &work}} {
		res := inForce(t, p, cp)
		b, _ := json.Marshal(res)
		if strings.Contains(string(b), "xyzzy") {
			t.Errorf("profile %v: %s", cp.Profile, b)
		}
		if _, ok := res.Config.Profiles["work"]; !ok || res.Default != "work" {
			t.Errorf("profile %v: %+v", cp.Profile, res)
		}
	}
	if res := inForce(t, p, rpc.ConfigParams{Profile: &work}); res.Config.Model != "w" || res.Config.Profile != "work" {
		t.Errorf("the profile asked for: %+v", res.Config)
	}
}

// The shell on the profile config.toml selects has the model it started
// with, $AISH_MODEL's, and the status shows no switch; the profile asked
// for by name has its own.
func TestInForceStartModel(t *testing.T) {
	p := configured(t, "profile = \"work\"\n\n[profiles.work]\nmodel = \"w\"\n")
	p.mu.Lock()
	p.profile = "work"
	p.mu.Unlock()
	work := "work"
	env := []string{"AISH_MODEL=x"}
	if res := inForce(t, p, rpc.ConfigParams{Env: env}); res.Config.Model != "x" || res.Default != "work" {
		t.Errorf("the shell's: %q, config.toml selects %q", res.Config.Model, res.Default)
	}
	if res := inForce(t, p, rpc.ConfigParams{Env: env, Profile: &work}); res.Config.Model != "w" {
		t.Errorf("by name: %q", res.Config.Model)
	}
}

// A profile added to config.toml is not the shell's to switch to until
// applied, and the refusal says how to apply it.
func TestInForceNewProfile(t *testing.T) {
	p := configured(t, "model = \"top\"\n")
	rewrite(t, "model = \"top\"\n\n[profiles.new]\nmodel = \"n\"\n")
	_, err := call(t, p, rpc.MethodModel, rpc.ModelParams{Profile: "new", Model: "n"})
	if err == nil || !strings.Contains(err.Error(), `no profile "new"`) || !strings.Contains(err.Error(), "aish apply-config") {
		t.Errorf("before apply-config: %v", err)
	}
	name := "new"
	if _, err := call(t, p, rpc.MethodConfig, rpc.ConfigParams{Profile: &name}); err == nil || !strings.Contains(err.Error(), "aish apply-config") {
		t.Errorf("its config before apply-config: %v", err)
	}
	apply(t, p, nil)
	if _, err := call(t, p, rpc.MethodModel, rpc.ModelParams{Profile: "new", Model: "n"}); err != nil {
		t.Errorf("after apply-config: %v", err)
	}
}

// The models are listed by the proxy, of the endpoint in force, with the
// key of config.toml or of the shell's environment.
func TestInForceModels(t *testing.T) {
	p := configured(t, "base_url = \"http://a\"\napi_key = \"k1\"\n\n[profiles.env]\nbase_url = \"http://e\"\napi_key_env = \"MY_KEY\"\n")
	made := answering(p)
	rewrite(t, "base_url = \"http://b\"\napi_key = \"k1\"\n")
	for _, mp := range []rpc.ModelsParams{{}, {Profile: "env", Env: []string{"MY_KEY=k2"}}} {
		if _, err := call(t, p, rpc.MethodModels, mp); err != nil {
			t.Fatal(err)
		}
	}
	got := made()
	if len(got) != 2 || got[0].BaseURL != "http://a" || got[0].APIKey != "k1" || got[1].BaseURL != "http://e" || got[1].APIKey != "k2" {
		t.Errorf("listed with %+v", got)
	}
}

// mcpStarted gives p the MCP servers of path as Run does.
func mcpStarted(t *testing.T, p *Proxy, path string) {
	t.Helper()
	servers, err := mcp.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.mcpFile, p.mcpSum = path, fileSum(path)
	p.mu.Unlock()
	p.mcp = mcp.NewManager(servers, filepath.Join(os.Getenv("XDG_CACHE_HOME"), "aish", "mcp"))
	t.Cleanup(p.mcp.Close)
}

// An edit of the MCP config is told of and applied by aish apply-config:
// a server gone stops, a new one is there, the rest go on; one that does
// not parse is not applied, nor the rest of the config with it.
func TestApplyConfigMCP(t *testing.T) {
	p := configured(t, "fold_lines = 3\n")
	t.Setenv("XDG_CACHE_HOME", filepath.Join(os.Getenv("HOME"), "cache"))
	path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "aish", "mcp.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(names ...string) {
		t.Helper()
		// Not started by Warm: their commands wait for the first call.
		s := "servers:\n"
		for _, n := range names {
			s += fmt.Sprintf("  %s:\n    command: /nonexistent/%s\n    env_command:\n      TOKEN: echo t\n", n, n)
		}
		if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	servers := func() []string {
		t.Helper()
		v, err := call(t, p, rpc.MethodMCPStatus, nil)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, s := range v.(mcp.StatusResult).Servers {
			names = append(names, s.Name)
		}
		return names
	}
	write("kept", "old")
	mcpStarted(t, p, path)

	write("kept", "new")
	ask(t, p, nil)
	ask(t, p, nil)
	if n := strings.Count(p.out.(*terminal).String(), changedLine); n != 1 {
		t.Errorf("the edit told %d times", n)
	}
	if res := inForce(t, p, rpc.ConfigParams{}); !slices.Equal(res.Changed, []string{path}) {
		t.Errorf("changed %q", res.Changed)
	}
	if got := servers(); !slices.Equal(got, []string{"kept", "old"}) {
		t.Errorf("before apply-config: %q", got)
	}

	res := apply(t, p, nil)
	if !slices.Contains(res.Files, path) || len(res.Restart) > 0 {
		t.Errorf("applied %+v", res)
	}
	if got := servers(); !slices.Equal(got, []string{"kept", "new"}) {
		t.Errorf("after apply-config: %q", got)
	}
	if res := inForce(t, p, rpc.ConfigParams{}); len(res.Changed) > 0 {
		t.Errorf("changed %q", res.Changed)
	}

	if err := os.WriteFile(path, []byte("servers: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rewrite(t, "fold_lines = 4\n")
	p.marker(Marker{Kind: "cmd-end", Payload: "0;/tmp"})
	if _, err := call(t, p, rpc.MethodApplyConfig, rpc.AgentParams{Cwd: filepath.Join(os.Getenv("HOME"), "work")}); err == nil ||
		!strings.Contains(err.Error(), "nothing applied") {
		t.Errorf("a broken MCP config: %v", err)
	}
	p.mu.Lock()
	cfg, _ := p.snapshot().LoadProfile("")
	p.mu.Unlock()
	if got := servers(); cfg.FoldLines != 3 || !slices.Equal(got, []string{"kept", "new"}) {
		t.Errorf("with a broken MCP config: fold_lines %d, servers %q", cfg.FoldLines, got)
	}
}
