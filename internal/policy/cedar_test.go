package policy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const permitAll = "permit(principal, action, resource);\n"

// load writes the files into a fresh directory and loads it.
func load(t *testing.T, files map[string]string) (*Engine, error) {
	t.Helper()
	dir := t.TempDir()
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return Load(context.Background(), dir, Rules{})
}

func mustLoad(t *testing.T, files map[string]string) *Engine {
	t.Helper()
	e, err := load(t, files)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func check(t *testing.T, e *Engine, in Input) Decision {
	t.Helper()
	d, err := e.Check(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func bash(cmd string) Input {
	return NewInput("bash", map[string]any{"command": cmd}, "/")
}

func TestTypoIsLoadError(t *testing.T) {
	_, err := load(t, map[string]string{"typo.cedar": permitAll +
		`forbid(principal, action == Action::"run", resource) when { context.comands.contains("x") };` + "\n"})
	if err == nil {
		t.Fatal("a policy with an unknown attribute loaded")
	}
	if !strings.Contains(err.Error(), "typo.cedar") || !strings.Contains(err.Error(), "comands") {
		t.Errorf("error does not name the file and the attribute: %v", err)
	}
}

func TestTwoFiles(t *testing.T) {
	e := mustLoad(t, map[string]string{
		"a.cedar": permitAll + `@reason("a") forbid(principal, action == Action::"run", resource == Command::"a");` + "\n",
		"b.cedar": `@reason("b") forbid(principal, action == Action::"run", resource == Command::"b");` + "\n",
	})
	n := 0
	for _, s := range e.Summary() {
		n += s.Policies
	}
	if n != 3 {
		t.Errorf("loaded %d policies, want 3: %+v", n, e.Summary())
	}
	for _, c := range []struct{ cmd, reason string }{{"a", "a"}, {"b", "b"}} {
		if d := check(t, e, bash(c.cmd)); d.Action != Deny || d.Reason != c.reason {
			t.Errorf("%s: %+v", c.cmd, d)
		}
	}
	if d := check(t, e, bash("c")); d.Action != Allow {
		t.Errorf("c: %+v", d)
	}
}

func TestVerdicts(t *testing.T) {
	e := mustLoad(t, map[string]string{"a.cedar": permitAll +
		"\n" +
		`forbid(principal, action == Action::"run", resource == Command::"plain");` + "\n" +
		`@ask("sure?") forbid(principal, action == Action::"run", resource == Command::"maybe");` + "\n" +
		`@ask("sure?") forbid(principal, action == Action::"run", resource == Command::"both");` + "\n" +
		`@reason("no") forbid(principal, action == Action::"run", resource == Command::"both");` + "\n"})
	for _, c := range []struct{ cmd, action, reason string }{
		{"plain", Deny, "a.cedar:3 forbid"},
		{"maybe", Ask, "sure?"},
		{"both", Deny, "no"},
		{"maybe; plain", Deny, "a.cedar:3 forbid"},
		{"maybe && maybe", Ask, "sure?"},
		{"ls", Allow, ""},
	} {
		if d := check(t, e, bash(c.cmd)); d.Action != c.action || d.Reason != c.reason {
			t.Errorf("%s: %+v, want %s %q", c.cmd, d, c.action, c.reason)
		}
	}
}

func TestNoPermitIsDeny(t *testing.T) {
	e := mustLoad(t, map[string]string{"a.cedar": `forbid(principal, action == Action::"run", resource == Command::"sudo");` + "\n"})
	d := check(t, e, bash("ls"))
	if d.Action != Deny || d.Reason != `no permit for run Command::"ls"` {
		t.Errorf("%+v", d)
	}
}

func TestRegoIsLoadError(t *testing.T) {
	_, err := load(t, map[string]string{
		"a.cedar":  permitAll,
		"old.rego": "package aish\n",
	})
	if err == nil || !strings.Contains(err.Error(), "old.rego") || !strings.Contains(err.Error(), "Cedar") {
		t.Errorf("a Rego file next to Cedar: %v", err)
	}
}

func TestMCPTool(t *testing.T) {
	e := mustLoad(t, map[string]string{"a.cedar": permitAll +
		`@reason("no github") forbid(principal, action == Action::"call", resource) when { resource in Server::"github" };` + "\n" +
		`@reason("public") forbid(principal, action == Action::"call", resource)
		 when { resource.hasTag("private") && resource.getTag("private") == "false" &&
		        resource.hasTag("name") && resource.getTag("name") == "secret" };` + "\n"})
	in := NewInput("create_repo", map[string]any{"name": "x"}, "/")
	in.Server = "github"
	if d := check(t, e, in); d.Action != Deny || d.Reason != "no github" {
		t.Errorf("github: %+v", d)
	}
	in = NewInput("create_repo", map[string]any{"name": "secret", "private": false}, "/")
	in.Server = "gitea"
	if d := check(t, e, in); d.Action != Deny || d.Reason != "public" {
		t.Errorf("public secret: %+v", d)
	}
	in.Args["private"] = true
	if d := check(t, e, in); d.Action != Allow {
		t.Errorf("private secret: %+v", d)
	}
	if d := check(t, e, NewInput("weather", map[string]any{"city": "Oslo"}, "/")); d.Action != Allow {
		t.Errorf("external tool: %+v", d)
	}
}

func TestModelPrincipal(t *testing.T) {
	e := mustLoad(t, map[string]string{"a.cedar": permitAll +
		`@reason("not for x") forbid(principal == Model::"x", action, resource);` + "\n"})
	in := bash("ls")
	in.Model = "x"
	if d := check(t, e, in); d.Action != Deny || d.Reason != "not for x" {
		t.Errorf("x: %+v", d)
	}
	in.Model = "y"
	if d := check(t, e, in); d.Action != Allow {
		t.Errorf("y: %+v", d)
	}
}

func TestHomeAndCwdDirs(t *testing.T) {
	e := mustLoad(t, map[string]string{"a.cedar": permitAll +
		`@reason("outside home") forbid(principal, action == Action::"write", resource) unless { resource in Dir::"~" };` + "\n" +
		`@reason("outside cwd") forbid(principal, action == Action::"read", resource) unless { resource in Dir::"." };` + "\n"})
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.Symlink("/etc", filepath.Join(home, "etc")); err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(home, "proj")
	for _, c := range []struct{ tool, path, want string }{
		{"write_file", "x", Allow},
		{"write_file", "deep/er/x", Allow},
		{"write_file", home, Allow},
		{"write_file", filepath.Join(home, "etc", "hosts"), Deny},
		{"write_file", "/etc/hosts", Deny},
		{"read_file", "x", Allow},
		{"read_file", "../x", Deny},
		{"read_file", "/etc/hosts", Deny},
	} {
		d := check(t, e, NewInput(c.tool, map[string]any{"path": c.path}, cwd))
		if d.Action != c.want {
			t.Errorf("%s %s: %+v, want %s", c.tool, c.path, d, c.want)
		}
	}
}

func TestParseErrorRequest(t *testing.T) {
	e := mustLoad(t, map[string]string{"a.cedar": permitAll +
		`@ask("unparsed") forbid(principal, action == Action::"run", resource) when { context has parse_error };` + "\n"})
	if d := check(t, e, bash("echo 'oops")); d.Action != Ask || d.Reason != "unparsed" {
		t.Errorf("%+v", d)
	}
	if d := check(t, e, bash("")); d.Action != Allow {
		t.Errorf("empty command: %+v", d)
	}
}

func TestCombine(t *testing.T) {
	d := combine([]Decision{
		{Action: Allow},
		{Action: Ask, Reason: "a"},
		{Action: Deny, Reason: "x; y"},
		{Action: Ask, Reason: "b"},
		{Action: Deny, Reason: "y; z"},
	})
	if d.Action != Deny || d.Reason != "x; y; z" {
		t.Errorf("%+v", d)
	}
	if d := combine(nil); d.Action != Allow || d.Reason != "" {
		t.Errorf("%+v", d)
	}
}
