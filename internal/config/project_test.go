package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// repo makes a git repository root with a project file and returns it.
func repo(t *testing.T, root, toml string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ProjectFile), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestProject(t *testing.T) {
	root := trustHome(t)
	repo(t, filepath.Join(root, "home", "src", "x"), strings.Join([]string{
		`system_prompt = "A Go service; test with make."`,
		`max_steps = 7`,
		`markdown = false`,
		`tools_dir = "tools"`,
		`hooks_dir = ".aish/hooks"`,
		`policy_dir = "/abs/policy"`,
	}, "\n")+"\n")
	sub := filepath.Join(root, "home", "src", "x", "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Trust(filepath.Join(root, "home", "src", "x", ProjectFile)); err != nil {
		t.Fatal(err)
	}
	base := Default()
	base.SystemPrompt = "Global."
	cfg, file, err := Project(base, sub)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "home", "src", "x", ProjectFile); file != want {
		t.Errorf("file %q, want %q (found from a subdirectory)", file, want)
	}
	if cfg.MaxSteps != 7 || cfg.Markdown || cfg.MaxOutputBytes != base.MaxOutputBytes {
		t.Errorf("keys not laid over: %+v", cfg)
	}
	if cfg.SystemPrompt != "Global.\n\nA Go service; test with make." {
		t.Errorf("system_prompt must be appended, got %q", cfg.SystemPrompt)
	}
	if want := base.ToolsDir + string(filepath.ListSeparator) + filepath.Join(root, "home", "src", "x", "tools"); cfg.ToolsDir != want {
		t.Errorf("tools_dir %q, want %q (relative to the file, after the global one)", cfg.ToolsDir, want)
	}
	if want := base.HooksDir + string(filepath.ListSeparator) + filepath.Join(root, "home", "src", "x", ".aish", "hooks"); cfg.HooksDir != want {
		t.Errorf("hooks_dir %q, want %q", cfg.HooksDir, want)
	}
	if want := base.PolicyDir + string(filepath.ListSeparator) + "/abs/policy"; cfg.PolicyDir != want {
		t.Errorf("policy_dir %q, want %q", cfg.PolicyDir, want)
	}
	if cfg.Untrusted != nil {
		t.Errorf("untrusted %q in a trusted file", cfg.Untrusted)
	}
}

// The keys that run code from the repository hold only once the file is
// trusted, as it is: the rest holds anyway.
func TestProjectUntrusted(t *testing.T) {
	root := trustHome(t)
	repo(t, root, "hooks_dir = \".aish/hooks\"\ntools_dir = \"tools\"\nmax_steps = 7\n")
	base := Default()
	cfg, file, err := Project(base, root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxSteps != 7 || cfg.HooksDir != base.HooksDir || cfg.ToolsDir != base.ToolsDir {
		t.Errorf("untrusted: max_steps %d, hooks_dir %q, tools_dir %q", cfg.MaxSteps, cfg.HooksDir, cfg.ToolsDir)
	}
	if want := []string{"hooks_dir", "tools_dir"}; !reflect.DeepEqual(cfg.Untrusted, want) {
		t.Errorf("Untrusted %q, want %q", cfg.Untrusted, want)
	}

	if err := Trust(file); err != nil {
		t.Fatal(err)
	}
	cfg, _, err = Project(base, root)
	if err != nil {
		t.Fatal(err)
	}
	sep := string(filepath.ListSeparator)
	if cfg.MaxSteps != 7 || cfg.HooksDir != base.HooksDir+sep+filepath.Join(root, ".aish", "hooks") ||
		cfg.ToolsDir != base.ToolsDir+sep+filepath.Join(root, "tools") || cfg.Untrusted != nil {
		t.Errorf("trusted: max_steps %d, hooks_dir %q, tools_dir %q, untrusted %q", cfg.MaxSteps, cfg.HooksDir, cfg.ToolsDir, cfg.Untrusted)
	}

	// An edit, a pull say, takes the trust back.
	repo(t, root, "hooks_dir = \"other\"\nmax_steps = 7\n")
	cfg, _, err = Project(base, root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HooksDir != base.HooksDir || !reflect.DeepEqual(cfg.Untrusted, []string{"hooks_dir"}) {
		t.Errorf("edited: hooks_dir %q, untrusted %q", cfg.HooksDir, cfg.Untrusted)
	}
}

func TestProjectPolicy(t *testing.T) {
	// A project can only make the policy stricter: its lists are added to
	// the global ones, and the stricter write_outside_home wins.
	for _, tc := range []struct{ global, project, want string }{
		{"", "deny", "deny"},
		{"ask", "allow", "ask"},
		{"deny", "ask", "deny"},
		{"ask", "", "ask"},
	} {
		root := t.TempDir()
		t.Setenv("HOME", filepath.Join(root, "home"))
		repo(t, root, "[policy]\ndeny = [\"make deploy*\"]\nask = [\"docker *\"]\nwrite_outside_home = \""+tc.project+"\"\n")
		base := Default()
		base.Policy = Policy{Deny: []string{"sudo *"}, Ask: []string{"git push*"}, WriteOutsideHome: tc.global}
		cfg, _, err := Project(base, root)
		if err != nil {
			t.Fatal(err)
		}
		want := Policy{
			Deny:             []string{"sudo *", "make deploy*"},
			Ask:              []string{"git push*", "docker *"},
			WriteOutsideHome: tc.want,
		}
		if !reflect.DeepEqual(cfg.Policy, want) {
			t.Errorf("global %q, project %q: %+v, want %+v", tc.global, tc.project, cfg.Policy, want)
		}
	}
	// Without [policy] the global one stays as it is.
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	repo(t, root, "max_steps = 3\n")
	base := Default()
	base.Policy = Policy{Deny: []string{"sudo *"}}
	if cfg, _, err := Project(base, root); err != nil || !reflect.DeepEqual(cfg.Policy, base.Policy) {
		t.Errorf("without [policy]: %+v, %v", cfg.Policy, err)
	}
}

func TestProjectStopsAtGit(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	// An outer repository's file must not reach a repository nested in it.
	repo(t, filepath.Join(root, "outer"), "max_steps = 1\n")
	inner := filepath.Join(root, "outer", "inner")
	if err := os.MkdirAll(filepath.Join(inner, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, file, err := Project(Default(), filepath.Join(inner))
	if err != nil {
		t.Fatal(err)
	}
	if file != "" || cfg.MaxSteps != Default().MaxSteps {
		t.Errorf("found %q through the inner repository's root", file)
	}
	// Without a .git in between the file applies.
	plain := filepath.Join(root, "outer", "plain", "deep")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, file, err := Project(Default(), plain); err != nil || file == "" {
		t.Errorf("not found from a plain subdirectory: %q, %v", file, err)
	}
}

func TestProjectIgnoresHome(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "work"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ProjectFile), []byte("max_steps = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ProjectFile), []byte("max_steps = 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, cwd := range []string{home, filepath.Join(home, "work")} {
		cfg, file, err := Project(Default(), cwd)
		if err != nil {
			t.Fatal(err)
		}
		if file != "" || cfg.MaxSteps != Default().MaxSteps {
			t.Errorf("%s: took %q; ~ and what is above it are not a project", cwd, file)
		}
	}
	// Outside the home directory the search goes up to the root.
	out := filepath.Join(root, "srv", "app")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if cfg, _, err := Project(Default(), out); err != nil || cfg.MaxSteps != 2 {
		t.Errorf("outside ~: max_steps %d, %v", cfg.MaxSteps, err)
	}
}

func TestProjectErrors(t *testing.T) {
	for _, tc := range []struct{ toml, want string }{
		{"model = \"x\"\n", `key "model" is not allowed in a project config`},
		{"base_url = \"http://x\"\n", `key "base_url" is not allowed`},
		{"api_key = \"k\"\n", `key "api_key" is not allowed`},
		{"max_steps = 3\nmax_stepz = 4\n", `unknown key "max_stepz"`},
		{"max_steps = -1\n", "max_steps = -1: must not be negative"},
		{"max_steps = \"3\"\n", "max_steps"},
		{"[policy]\nwrite_outside_home = \"never\"\n", `policy.write_outside_home = "never"`},
		{"[policy]\nallow = [\"sudo *\"]\n", `unknown key "policy.allow"`},
	} {
		root := t.TempDir()
		t.Setenv("HOME", filepath.Join(root, "home"))
		repo(t, root, tc.toml)
		_, file, err := Project(Default(), root)
		if err == nil {
			t.Errorf("%q: no error", tc.toml)
			continue
		}
		if msg := err.Error(); !strings.Contains(msg, tc.want) || !strings.Contains(msg, file) {
			t.Errorf("%q: %v, want %q and the file %s", tc.toml, err, tc.want, file)
		}
	}
}

func TestProjectNone(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	base := Default()
	base.SystemPrompt = "Global."
	cfg, file, err := Project(base, root)
	if err != nil || file != "" || !reflect.DeepEqual(cfg, base) {
		t.Errorf("without a file: %q, %v, %+v", file, err, cfg)
	}
}
