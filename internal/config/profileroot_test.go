package config

import "testing"

// root, the top level's name in aish model, names it in the config and in
// $AISH_PROFILE too.
func TestProfileRoot(t *testing.T) {
	cfg, err := load(t, "model = \"top\"\nprofile = \"root\"\n\n[profiles.work]\nmodel = \"other\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Profile != "" || cfg.Model != "top" {
		t.Errorf("profile = %q: profile %q, model %q; want the top level", Root, cfg.Profile, cfg.Model)
	}

	t.Setenv("AISH_PROFILE", Root)
	for _, toml := range []string{
		"model = \"top\"\nprofile = \"work\"\n\n[profiles.work]\nmodel = \"other\"\n",
		"model = \"top\"\n",
	} {
		cfg, err := load(t, toml)
		if err != nil {
			t.Fatalf("$AISH_PROFILE=%s over %q: %v", Root, toml, err)
		}
		if cfg.Profile != "" || cfg.Model != "top" {
			t.Errorf("$AISH_PROFILE=%s over %q: profile %q, model %q; want the top level", Root, toml, cfg.Profile, cfg.Model)
		}
	}
	if cfg, err := LoadProfile(Root); err != nil || cfg.Profile != "" || cfg.Model != "top" {
		t.Errorf("LoadProfile(%q): profile %q, model %q, %v; want the top level", Root, cfg.Profile, cfg.Model, err)
	}
}

// The top level's effort goes only where its provider does: another one
// may have no such level.
func TestProfileEffort(t *testing.T) {
	const toml = `
effort = "max"

[profiles.o]
provider = "openai"

[profiles.oh]
provider = "openai"
effort = "high"

[profiles.b]
base_url = "http://localhost:8317"
`
	if _, err := load(t, toml); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ profile, effort string }{
		{"o", ""},
		{"oh", "high"},
		{"b", "max"},
		{"", "max"},
	} {
		cfg, err := LoadProfile(tc.profile)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Effort != tc.effort {
			t.Errorf("profile %q: effort %q, want %q", tc.profile, cfg.Effort, tc.effort)
		}
	}
}
