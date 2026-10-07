package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/inebotov/aish/internal/config"
	"github.com/inebotov/aish/internal/hooks"
	"github.com/inebotov/aish/internal/mcp"
	"github.com/inebotov/aish/internal/policy"
	"github.com/inebotov/aish/internal/rpc"
)

// fakeProxy answers the rpc of the commands at $AISH_SOCK by answers, by
// method, and records the parameters of each call.
func fakeProxy(t *testing.T, answers map[string]any) map[string]json.RawMessage {
	t.Helper()
	l, err := net.Listen("unix", filepath.Join(t.TempDir(), "sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	var mu sync.Mutex
	got := map[string]json.RawMessage{}
	go rpc.Serve(l, func(_ context.Context, method string, params json.RawMessage) (any, error) {
		mu.Lock()
		got[method] = params
		mu.Unlock()
		a, ok := answers[method]
		if !ok {
			return nil, errors.New("unexpected rpc " + method)
		}
		if err, ok := a.(error); ok {
			return nil, err
		}
		return a, nil
	})
	t.Setenv("AISH_SOCK", l.Addr().String())
	return got
}

// diskConfig is a config.toml on disk with body, as an edit not applied
// leaves it, and what run reads of it.
func diskConfig(t *testing.T, body string) config.Config {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("AISH_PROFILE", "")
	t.Setenv("AISH_MODEL", "")
	t.Setenv("AISH_EFFORT", "")
	path := filepath.Join(root, "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AISH_CONFIG", path)
	cfg, _ := config.LoadEnv(os.Getenv)
	work := filepath.Join(root, "work")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(work)
	return cfg
}

// aish policy inside aish answers by the policies in force, the proxy's,
// not by an edit on disk, and names the edit.
func TestPolicyInForce(t *testing.T) {
	disk := diskConfig(t, "[policy]\ndeny = [\"rm *\"]\n")
	applied := config.Default()
	applied.Policy.Ask = []string{"git push*"}
	got := fakeProxy(t, map[string]any{
		rpc.MethodConfig: rpc.Config{Config: applied, Global: 1, Changed: []string{os.Getenv("AISH_CONFIG")},
			Policies: []policy.Summary{{File: "a.cedar", Policies: 2}}},
		rpc.MethodMCPList: mcp.ListResult{},
		rpc.MethodPolicy:  policy.Decision{Action: policy.Allow},
	})
	code, out, stderr := captured(t, func() int { return policyCmd(disk, []string{"bash", "rm x"}) })
	if code != 0 || out != "allow\n" || !strings.Contains(stderr, "config changed on disk: aish apply-config") {
		t.Errorf("exit %d, stdout %q, stderr %q", code, out, stderr)
	}
	var pp rpc.PolicyParams
	if err := json.Unmarshal(got[rpc.MethodPolicy], &pp); err != nil {
		t.Fatal(err)
	}
	cwd, _ := os.Getwd()
	if pp.Tool != "bash" || !pp.HandOff || pp.Line != "rm x" || pp.Cwd != cwd {
		t.Errorf("asked %+v", pp)
	}

	code, out, _ = captured(t, func() int { return policyCmd(disk, nil) })
	if want := "2 policies in ~/.config/aish/policy: a.cedar (2) + 1 rule from config.toml\n"; code != 0 || out != want {
		t.Errorf("exit %d, %q, want %q", code, out, want)
	}
}

// aish status inside aish shows the config in force and says the files
// on disk differ.
func TestStatusInForce(t *testing.T) {
	disk := diskConfig(t, "fold_lines = 9\n")
	applied := config.Default()
	applied.FoldLines = 3
	fakeProxy(t, map[string]any{
		rpc.MethodStatus:  rpc.Status{Info: rpc.Info{Model: applied.Model}},
		rpc.MethodConfig:  rpc.Config{Config: applied, Changed: []string{os.Getenv("AISH_CONFIG")}},
		rpc.MethodMCPList: mcp.ListResult{},
	})
	code, out, stderr := captured(t, func() int { return statusCmd(disk) })
	out = ansi.ReplaceAllString(out, "")
	if code != 0 || !strings.Contains(out, "fold_lines       3\n") ||
		!strings.Contains(out, "config changed on disk: aish apply-config to apply ~/config.toml") {
		t.Errorf("exit %d, stdout:\n%s\nstderr %q", code, out, stderr)
	}
}

// aish model PROFILE with a profile config.toml has on disk only says to
// apply it rather than take it for a model.
func TestModelNotInForce(t *testing.T) {
	disk := diskConfig(t, "[profiles.new]\nmodel = \"n\"\n")
	got := fakeProxy(t, map[string]any{
		rpc.MethodInfo:   rpc.Info{Model: "m"},
		rpc.MethodConfig: rpc.Config{Config: config.Default()},
	})
	code, _, stderr := captured(t, func() int { return modelCmd(disk, []string{"new"}) })
	if code == 0 || !strings.Contains(stderr, `no profile "new" in the config in force; aish apply-config`) {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
	if _, ok := got[rpc.MethodModel]; ok {
		t.Error("switched")
	}
}

// aish hooks inside aish lists the hooks of the hooks_dir in force.
func TestHooksInForce(t *testing.T) {
	disk := diskConfig(t, "")
	disk.HooksDir = filepath.Join(os.Getenv("HOME"), "disk")
	writeHook(t, disk.HooksDir, hooks.PreTool, "on-disk", 0o755)
	applied := config.Default()
	applied.HooksDir = filepath.Join(os.Getenv("HOME"), "applied")
	writeHook(t, applied.HooksDir, hooks.PreTool, "in-force", 0o755)
	fakeProxy(t, map[string]any{rpc.MethodConfig: rpc.Config{Config: applied}})
	code, out, _ := captured(t, func() int { return hooksCmd(disk, nil) })
	if code != 0 || !strings.Contains(out, "in-force") || strings.Contains(out, "on-disk") {
		t.Errorf("exit %d, %q", code, out)
	}
}

// A config.toml broken on disk, which aish apply-config refuses, stops
// neither a request nor the commands that go by the config in force inside
// aish; outside aish it stops them, as before.
func TestRunBrokenOnDisk(t *testing.T) {
	diskConfig(t, "max_steps = -1\n")
	fakeProxy(t, map[string]any{
		rpc.MethodAgentStart: nil,
		rpc.MethodStatus:     rpc.Status{Info: rpc.Info{Model: "claude-opus-5"}},
		rpc.MethodConfig:     rpc.Config{Config: config.Default()},
		rpc.MethodMCPList:    mcp.ListResult{},
	})
	for _, args := range [][]string{{"agent", "start", "--", "hi"}, {"status"}, {"hooks"}} {
		if code, _, stderr := captured(t, func() int { return run(args) }); code != 0 || stderr != "" {
			t.Errorf("aish %q inside aish: exit %d, stderr %q", args, code, stderr)
		}
	}
	t.Setenv("AISH_SOCK", "")
	if code, _, stderr := captured(t, func() int { return run([]string{"hooks"}) }); code == 0 || !strings.Contains(stderr, "max_steps") {
		t.Errorf("outside aish: exit %d, stderr %q", code, stderr)
	}
}
