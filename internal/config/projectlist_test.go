package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// A directory key of a project names one of its directories: a list would
// take its second element from wherever it points, or, relative, from the
// proxy's cwd. Refused whether the file is trusted or not, as a key not
// allowed is.
func TestProjectDirList(t *testing.T) {
	sep := string(filepath.ListSeparator)
	for _, key := range []string{"tools_dir", "hooks_dir", "policy_dir"} {
		for _, value := range []string{"a" + sep + "b", "tools" + sep + "/opt/x", sep + "a"} {
			for _, trust := range []bool{false, true} {
				root := trustHome(t)
				repo(t, root, key+" = \""+value+"\"\n")
				path := filepath.Join(root, ProjectFile)
				if trust {
					if err := Trust(path); err != nil {
						t.Fatal(err)
					}
				}
				cfg, file, err := Project(Default(), root)
				if err == nil {
					t.Errorf("%s = %q (trusted %v): no error, tools_dir %q, hooks_dir %q, policy_dir %q",
						key, value, trust, cfg.ToolsDir, cfg.HooksDir, cfg.PolicyDir)
					continue
				}
				if msg := err.Error(); file != path || !strings.Contains(msg, path) ||
					!strings.Contains(msg, key) || !strings.Contains(msg, "one directory, not a list") {
					t.Errorf("%s = %q (trusted %v): %q, file %q", key, value, trust, msg, file)
				}
			}
		}
	}
}

// One directory is laid over as before: relative to the project file,
// after the global ones.
func TestProjectDirOne(t *testing.T) {
	sep := string(filepath.ListSeparator)
	root := trustHome(t)
	repo(t, root, "tools_dir = \"a\"\nhooks_dir = \"/abs/hooks\"\npolicy_dir = \"p\"\n")
	if err := Trust(filepath.Join(root, ProjectFile)); err != nil {
		t.Fatal(err)
	}
	base := Default()
	cfg, _, err := Project(base, root)
	if err != nil {
		t.Fatal(err)
	}
	if want := base.ToolsDir + sep + filepath.Join(root, "a"); cfg.ToolsDir != want {
		t.Errorf("tools_dir %q, want %q", cfg.ToolsDir, want)
	}
	if want := base.HooksDir + sep + "/abs/hooks"; cfg.HooksDir != want {
		t.Errorf("hooks_dir %q, want %q", cfg.HooksDir, want)
	}
	if want := base.PolicyDir + sep + filepath.Join(root, "p"); cfg.PolicyDir != want {
		t.Errorf("policy_dir %q, want %q", cfg.PolicyDir, want)
	}
	if cfg.Untrusted != nil {
		t.Errorf("untrusted %q in a trusted file", cfg.Untrusted)
	}
}
