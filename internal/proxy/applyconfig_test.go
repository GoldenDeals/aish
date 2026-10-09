package proxy

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/GoldenDeals/aish/internal/agent"
	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/policy"
	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/shellstate"
)

// apply is `aish apply-config` typed at the prompt of the shell, in its
// work directory, with env its environment (nil: the test's), and its
// question answered Yes.
func apply(t *testing.T, p *Proxy, env []string) rpc.Applied {
	t.Helper()
	cwd := filepath.Join(os.Getenv("HOME"), "work")
	p.marker(Marker{Kind: "cmd-end", Payload: "0;" + cwd}) // back at the prompt
	res, err := applyAnswered(t, p, rpc.AgentParams{Cwd: cwd, Env: env}, "y")
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// configured is a proxy whose config.toml is toml, read as Run reads it.
func configured(t *testing.T, toml string) *Proxy {
	t.Helper()
	t.Setenv("AISH_PROFILE", "")
	os.Unsetenv("AISH_PROFILE")
	p, _, _ := hosted(t, &scripted{})
	answering(p)
	rewrite(t, toml)
	conf := config.NewSnapshot()
	cfg, err := conf.LoadEnv(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.conf, p.started = conf, &cfg
	p.applyFields(cfg)
	p.mu.Unlock()
	return p
}

// verdict is what the policies of the request in progress, or the last,
// say of command.
func verdict(t *testing.T, a *agent.Agent, command string) string {
	t.Helper()
	in := policy.NewInput("bash", map[string]any{"command": command}, filepath.Join(os.Getenv("HOME"), "work"), nil)
	in.HandOff(command)
	d, err := a.Policy.Check(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return d.Action
}

const changedLine = "\x1b[2m[aish: config changed on disk: aish apply-config to apply it]\x1b[0m\r\n"

// An edit of config.toml changes nothing until aish apply-config, and then
// all of it at once: the agent and the proxy go by the same fold_lines.
func TestApplyConfig(t *testing.T) {
	p := configured(t, "fold_lines = 3\nsystem_prompt = \"Be brief.\"\n\n[policy]\ndeny = [\"rm *\"]\n")
	ask(t, p, nil)
	a := p.ag
	if a.Cfg.FoldLines != 3 || p.foldLines != 3 || a.Cfg.SystemPrompt != "Be brief." || verdict(t, a, "rm x") != policy.Deny {
		t.Fatalf("as started: %d %d %q", a.Cfg.FoldLines, p.foldLines, a.Cfg.SystemPrompt)
	}

	rewrite(t, "fold_lines = 5\nsystem_prompt = \"Be thorough.\"\nstate_ignore = [\"X\"]\n\n[policy]\ndeny = [\"ls *\"]\n")
	ask(t, p, nil)
	ask(t, p, nil)
	if a.Cfg.FoldLines != 3 || p.foldLines != 3 || a.Cfg.SystemPrompt != "Be brief." ||
		verdict(t, a, "rm x") != policy.Deny || verdict(t, a, "ls x") != policy.Allow {
		t.Errorf("before apply-config: %d %d %q", a.Cfg.FoldLines, p.foldLines, a.Cfg.SystemPrompt)
	}
	if n := strings.Count(p.out.(*terminal).String(), changedLine); n != 1 {
		t.Errorf("the edit told %d times", n)
	}

	p.mu.Lock()
	p.base = &shellstate.State{} // state_ignore changed: parsed again at the next prompt
	p.mu.Unlock()
	res := apply(t, p, nil)
	if want := []string{"fold_lines", "policy.deny", "state_ignore", "system_prompt"}; !slices.Equal(res.Keys, want) {
		t.Errorf("keys %q, want %q", res.Keys, want)
	}
	if len(res.Restart) > 0 || res.Switched {
		t.Errorf("applied %+v", res)
	}
	p.mu.Lock()
	base, ignore := p.base, p.stateIgnore
	p.mu.Unlock()
	if base != nil || !slices.Equal(ignore, []string{"X"}) {
		t.Errorf("state_ignore %q, base %v", ignore, base)
	}
	ask(t, p, nil)
	if a.Cfg.FoldLines != 5 || p.foldLines != 5 || a.Cfg.SystemPrompt != "Be thorough." ||
		verdict(t, a, "rm x") != policy.Allow || verdict(t, a, "ls x") != policy.Deny {
		t.Errorf("after apply-config: %d %d %q", a.Cfg.FoldLines, p.foldLines, a.Cfg.SystemPrompt)
	}

	// What only a restart applies is named, and stays named until then.
	rewrite(t, "fold_lines = 5\nshell = \"/bin/sh\"\n\n[route]\nsuffix = \"??\"\n")
	for range 2 {
		if res := apply(t, p, nil); !slices.Equal(res.Restart, []string{"shell", "route"}) {
			t.Errorf("restart %q", res.Restart)
		}
	}
}

// A config.toml that does not parse is not applied: the one before stays
// in force, whole.
func TestApplyConfigBroken(t *testing.T) {
	p := configured(t, "fold_lines = 3\n")
	ask(t, p, nil)
	for _, toml := range []string{"fold_lines = [\n", "fold_lines = 4\nnosuch = 1\n", "fold_lines = 4\nprofile = \"gone\"\n"} {
		rewrite(t, toml)
		p.marker(Marker{Kind: "cmd-end", Payload: "0;/tmp"})
		_, err := applyAnswered(t, p, rpc.AgentParams{Cwd: filepath.Join(os.Getenv("HOME"), "work")}, "y")
		if toml == "fold_lines = 4\nprofile = \"gone\"\n" {
			// The profile it selects is not the shell's to fail on: told as a request tells it.
			if err != nil {
				t.Errorf("%q: %v", toml, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), "nothing applied") {
			t.Errorf("%q: %v", toml, err)
		}
		ask(t, p, nil)
		if p.ag.Cfg.FoldLines != 3 || p.foldLines != 3 {
			t.Errorf("%q: fold_lines %d %d", toml, p.ag.Cfg.FoldLines, p.foldLines)
		}
	}
}

// A policy edited, or broken, is not in force until applied; broken, it is
// not applied.
func TestApplyConfigPolicies(t *testing.T) {
	p := configured(t, "")
	dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "aish", "policy")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(src string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "a.cedar"), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	const deny = "permit(principal, action, resource);\n@reason(\"no\") forbid(principal, action == Action::\"run\", resource) when { context.program == \"%s\" };\n"
	write(strings.ReplaceAll(deny, "%s", "rm"))
	ask(t, p, nil)
	first := p.ag.Policy
	if verdict(t, p.ag, "rm x") != policy.Deny {
		t.Fatal("the policy is not in force")
	}
	write(strings.ReplaceAll(deny, "%s", "ls") + "// edited\n")
	ask(t, p, nil)
	if p.ag.Policy != first || verdict(t, p.ag, "ls x") != policy.Allow {
		t.Error("an edited policy is in force before apply-config")
	}
	if !strings.Contains(p.out.(*terminal).String(), changedLine) {
		t.Error("the edit is not told")
	}

	write("permit(principal, action, resource\n")
	p.marker(Marker{Kind: "cmd-end", Payload: "0;/tmp"})
	if _, err := call(t, p, rpc.MethodApplyConfig, rpc.AgentParams{Cwd: filepath.Join(os.Getenv("HOME"), "work")}); err == nil {
		t.Error("a broken policy was applied")
	}
	ask(t, p, nil)
	if p.ag.Policy != first {
		t.Error("the policies in force changed with a broken one")
	}

	write(strings.ReplaceAll(deny, "%s", "ls"))
	res := apply(t, p, nil)
	if !slices.Contains(res.Files, dir) {
		t.Errorf("files %q", res.Files)
	}
	ask(t, p, nil)
	if verdict(t, p.ag, "ls x") != policy.Deny || verdict(t, p.ag, "rm x") != policy.Allow {
		t.Error("the policy applied is not in force")
	}
}

// A project file is read the first time a request needs it: one new in a
// directory asked about, or edited, is in force once applied. Its trust is
// not kept: an edit turns its hooks and tools off at the next step, while
// its other keys stay as they were until applied.
func TestApplyConfigProject(t *testing.T) {
	p := configured(t, "")
	cwd := filepath.Join(os.Getenv("HOME"), "work")
	if err := os.Mkdir(filepath.Join(cwd, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	put := func(name, s string, mode os.FileMode) {
		t.Helper()
		path := filepath.Join(cwd, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(s), mode); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(cwd, config.ProjectFile)
	ask(t, p, nil)
	put(config.ProjectFile, "max_steps = 7\ntools_dir = \"tools\"\n", 0o644)
	put("tools/greet", "#!/bin/sh\n# aish:desc Greet.\necho hi\n", 0o755)
	if err := config.Trust(file); err != nil {
		t.Fatal(err)
	}
	ask(t, p, nil)
	if p.ag.Cfg.MaxSteps == 7 {
		t.Error("a new project file is in force before apply-config")
	}
	if res := apply(t, p, nil); !slices.Contains(res.Files, file) {
		t.Errorf("files %q", res.Files)
	}
	ask(t, p, nil)
	if _, ok := p.ag.Tools.Get("greet"); p.ag.Cfg.MaxSteps != 7 || !ok {
		t.Fatalf("applied: max_steps %d, the trusted tool there %v", p.ag.Cfg.MaxSteps, ok)
	}

	// Edited: not trusted any more, at once; max_steps stays.
	put(config.ProjectFile, "max_steps = 9\ntools_dir = \"tools\"\n", 0o644)
	ask(t, p, nil)
	if _, ok := p.ag.Tools.Get("greet"); ok || p.ag.Cfg.MaxSteps != 7 || !slices.Equal(p.ag.Cfg.Untrusted, []string{"tools_dir"}) {
		t.Errorf("edited: max_steps %d, untrusted %q, the tool there %v", p.ag.Cfg.MaxSteps, p.ag.Cfg.Untrusted, ok)
	}
	// Trusted as it is now, it is still not the file read: aish apply-config.
	if err := config.Trust(file); err != nil {
		t.Fatal(err)
	}
	ask(t, p, nil)
	if _, ok := p.ag.Tools.Get("greet"); ok {
		t.Error("the trust of the edit holds for the file read before it")
	}
	apply(t, p, nil)
	ask(t, p, nil)
	if _, ok := p.ag.Tools.Get("greet"); !ok || p.ag.Cfg.MaxSteps != 9 {
		t.Errorf("trusted and applied: max_steps %d, the tool there %v", p.ag.Cfg.MaxSteps, ok)
	}
	// Revoked: off at once.
	if err := config.Untrust(file); err != nil {
		t.Fatal(err)
	}
	ask(t, p, nil)
	if _, ok := p.ag.Tools.Get("greet"); ok {
		t.Error("the tool of a file no longer trusted is there")
	}
}

// Only the user applies the config: the assistant's command is refused, and
// a process in the background of the shell.
func TestApplyConfigUserOnly(t *testing.T) {
	p := configured(t, "fold_lines = 3\n")
	rewrite(t, "fold_lines = 4\n")
	ap := rpc.AgentParams{Cwd: filepath.Join(os.Getenv("HOME"), "work")}
	p.marker(Marker{Kind: "ask-start"})
	if _, err := call(t, p, rpc.MethodApplyConfig, ap); err != errApplyAsks {
		t.Errorf("the assistant: %v", err)
	}
	p.marker(Marker{Kind: "cmd-end", Payload: "0;/tmp"})
	p.mu.Lock()
	p.fg = func() (int, error) { return syscall.Getpgrp() + 1, nil }
	p.mu.Unlock()
	if _, err := call(t, p, rpc.MethodApplyConfig, ap); err != errNotShell {
		t.Errorf("from the background: %v", err)
	}
	p.mu.Lock()
	conf := p.conf
	p.mu.Unlock()
	if c, _ := conf.LoadProfile(""); c.FoldLines != 3 {
		t.Errorf("applied anyway: fold_lines %d", c.FoldLines)
	}
}

// The shell's model and effort follow an edit of config.toml where they
// are what it gave them, and stay where `aish model` chose them; so does
// the profile config.toml selects.
func TestApplyConfigFollow(t *testing.T) {
	p, _ := profiled(t)
	t.Setenv("AISH_PROFILE", "")
	os.Unsetenv("AISH_PROFILE")
	answering(p)
	ask(t, p, nil)
	shell := func() (string, string, string) {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.profile, p.model, p.effort
	}

	rewrite(t, strings.Replace(profilesTOML, `model = "claude-opus-5"`, `model = "claude-sonnet-5"`, 1))
	if res := apply(t, p, nil); !res.Switched || res.Info.Model != "claude-sonnet-5" {
		t.Errorf("applied %+v", res)
	}
	if pr, m, _ := shell(); pr != "work" || m != "claude-sonnet-5" {
		t.Errorf("the profile's model edited: %s %s", pr, m)
	}

	// Chosen with aish model, it stays.
	if _, err := call(t, p, rpc.MethodModel, rpc.ModelParams{Profile: "work", Model: "claude-haiku-5", Effort: "high"}); err != nil {
		t.Fatal(err)
	}
	rewrite(t, strings.Replace(profilesTOML, `model = "claude-opus-5"`, `model = "claude-fable-5"`, 1))
	if res := apply(t, p, nil); res.Switched {
		t.Errorf("applied %+v", res)
	}
	if pr, m, e := shell(); pr != "work" || m != "claude-haiku-5" || e != "high" {
		t.Errorf("chosen: %s %s %s", pr, m, e)
	}

	// The profile config.toml selects: the shell on it goes to the next.
	rewrite(t, strings.Replace(profilesTOML, `profile = "work"`, `profile = "local"`, 1))
	if res := apply(t, p, nil); !res.Switched || res.Info.Profile != "local" {
		t.Errorf("applied %+v", res)
	}
	if pr, m, e := shell(); pr != "local" || m != "qwen3:32b" || e != "low" {
		t.Errorf("selected: %s %s %s", pr, m, e)
	}
	p.mu.Lock()
	window, def := p.window, p.defProfile
	p.mu.Unlock()
	if window != 32000 || def != "local" {
		t.Errorf("window %d, config.toml selects %q", window, def)
	}
}
