package config

import (
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

// A value set in two files is an error naming both, whichever includes
// which: nested in maps too.
func TestMergeConflict(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "config.yaml")
	work := filepath.Join(dir, "profiles", "work.yaml")
	secrets := filepath.Join(dir, "secrets.yaml")
	for _, tc := range []struct {
		files map[string]string
		want  string
	}{{
		map[string]string{"config.yaml": "max_steps: 3\nmodel: a\ninclude: secrets.yaml\n", "secrets.yaml": "\nmodel: b\n"},
		secrets + ":2: model is also set in " + root + ":2",
	}, {
		map[string]string{
			"config.yaml":        "include: [profiles/*.yaml, secrets.yaml]\n",
			"profiles/work.yaml": "profiles:\n  work:\n    provider: openai\n    model: x\n",
			"secrets.yaml":       "profiles:\n  work:\n    api_key: k\n    model: x\n",
		},
		secrets + ":4: profiles.work.model is also set in " + work + ":4",
	}, {
		// A list that is no set is a value.
		map[string]string{"config.yaml": "include: secrets.yaml\nstate_ignore: [A]\n", "secrets.yaml": "policy: {hints: {x: y}}\nstate_ignore: [B]\n"},
		secrets + ":2: state_ignore is also set in " + root + ":2",
	}, {
		map[string]string{"config.yaml": "include: secrets.yaml\npolicy:\n  hints:\n    \"sudo *\": a\n", "secrets.yaml": "policy: {hints: {\"sudo *\": b}}\n"},
		secrets + ":1: policy.hints.sudo * is also set in " + root + ":4",
	}, {
		// A map and a value, a null and a value, a list and a null.
		map[string]string{"config.yaml": "route: {}\ninclude: secrets.yaml\n", "secrets.yaml": "route: true\n"},
		secrets + ":1: route is also set in " + root + ":1",
	}, {
		map[string]string{"config.yaml": "model:\ninclude: secrets.yaml\n", "secrets.yaml": "model: x\n"},
		secrets + ":1: model is also set in " + root + ":1",
	}, {
		map[string]string{"config.yaml": "mask: [a]\ninclude: secrets.yaml\n", "secrets.yaml": "mask:\n"},
		secrets + ":1: mask is also set in " + root + ":1",
	}} {
		files(t, dir, tc.files)
		c, err := readChain(root, "")
		if err != nil {
			t.Fatal(err)
		}
		// The sets other than state_ignore, for the list above.
		_, err = mergeChain(c, []string{"policy.deny", "policy.ask", "mask", "journal_ignore"})
		wantErr(t, err, tc.want)
	}
}

// The lists of rules add up without repeats, in the order read; the maps
// merge by their keys, a profile and its key in two files too.
func TestMergeSets(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "config.yaml")
	files(t, dir, map[string]string{
		"config.yaml": "include: [a.yaml, b.yaml]\npolicy:\n  deny: [\"sudo *\", \"rm -rf /\"]\n  write_outside_home: ask\nmask: ['tok_(\\w+)']\n",
		"a.yaml":      "include: c.yaml\npolicy:\n  deny:\n    - \"git push*\"\n    - \"sudo *\"\n  ask: [\"curl *\"]\njournal_ignore: [env, env]\nprofiles:\n  work:\n    model: m\n",
		"b.yaml":      "policy: {deny: [\"rm -rf /\", \"dd *\"], hints: {\"dd *\": no dd}}\nroute: {suffix: \"?!\"}\nprofiles: {work: {api_key: k}}\n",
		"c.yaml":      "",
	})
	cfg := Default()
	tr, err := loadYAML(root, "", &cfg, decodeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"sudo *", "rm -rf /", "git push*", "dd *"}; !slices.Equal(cfg.Policy.Deny, want) {
		t.Fatalf("deny %q, want %q", cfg.Policy.Deny, want)
	}
	if !slices.Equal(cfg.Policy.Ask, []string{"curl *"}) || cfg.Policy.WriteOutsideHome != "ask" || cfg.Policy.Hints["dd *"] != "no dd" {
		t.Fatalf("policy %+v", cfg.Policy)
	}
	// Set in a file, a list is no longer the default.
	if !slices.Equal(cfg.JournalIgnore, []string{"env"}) || !slices.Equal(cfg.Mask, []string{`tok_(\w+)`}) {
		t.Fatalf("journal_ignore %q, mask %q", cfg.JournalIgnore, cfg.Mask)
	}
	if !slices.Equal(cfg.StateIgnore, Default().StateIgnore) {
		t.Fatalf("state_ignore %q", cfg.StateIgnore)
	}
	// A key of a map in one file keeps the others' defaults.
	if want := (Route{Capital: true, NotFound: true, Suffix: "?!", MinWords: 2}); cfg.Route != want {
		t.Fatalf("route %+v", cfg.Route)
	}
	w := cfg.Profiles["work"]
	if w.Model == nil || *w.Model != "m" || w.APIKey == nil || *w.APIKey != "k" || w.Provider != nil {
		t.Fatalf("profile work %+v", w)
	}

	for key, want := range map[string]string{
		"policy":                    root + ":2",
		"policy.deny":               root + ":3",
		"policy.hints.dd *":         filepath.Join(dir, "b.yaml") + ":1",
		"profiles.work.model":       filepath.Join(dir, "a.yaml") + ":10",
		"profiles.work.api_key":     filepath.Join(dir, "b.yaml") + ":3",
		"route.suffix":              filepath.Join(dir, "b.yaml") + ":2",
		"policy.write_outside_home": root + ":4",
		"route.capital":             "",
		"include":                   "",
	} {
		if got := tr.at(key); got != want {
			t.Errorf("at(%q) = %q, want %q", key, got, want)
		}
	}
	if got, want := tr.itemAt("policy.deny", "git push*"), filepath.Join(dir, "a.yaml")+":4"; got != want {
		t.Errorf("itemAt(git push*) = %q, want %q", got, want)
	}
	if got, want := tr.itemAt("policy.deny", "rm -rf /"), root+":3"; got != want {
		t.Errorf("itemAt(rm -rf /) = %q, want %q", got, want)
	}
}

// A profile with nothing under it is a profile with nothing set, merged
// with what another file sets in it; null for a value is an error.
func TestMergeNull(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "config.yaml")
	files(t, dir, map[string]string{
		"config.yaml": "profiles:\n  empty:\n  work:\ninclude: [a.yaml, b.yaml]\nroute:\n",
		"a.yaml":      "profiles:\n  work:\n    model: m\n  empty:\n",
		"b.yaml":      "profiles:\n",
	})
	var cfg Config
	if _, err := loadYAML(root, "", &cfg, decodeOpts{}); err != nil {
		t.Fatal(err)
	}
	if e, ok := cfg.Profiles["empty"]; !ok || !reflect.ValueOf(e).IsZero() {
		t.Fatalf("profile empty: %+v, %v", e, ok)
	}
	if w := cfg.Profiles["work"]; w.Model == nil || *w.Model != "m" {
		t.Fatalf("profile work %+v", w)
	}
	for _, tc := range []struct{ data, want string }{
		{"model:\n", `:1: model: use "" for an empty string`},
		{"model: ~\n", `:1: model: use "" for an empty string`},
		{"profiles:\n  work:\n    model: null\n", `:3: profiles.work.model: use "" for an empty string`},
		{"mask:\n", ":1: mask: use [] for an empty list"},
		{"max_steps:\n", ":1: max_steps: want a whole number"},
		{"markdown:\n", ":1: markdown: want true or false"},
		{"journal_ignore: [env, ~]\n", ":1: journal_ignore: want a list of strings"},
	} {
		files(t, dir, map[string]string{"config.yaml": tc.data})
		_, err := loadYAML(root, "", &Config{}, decodeOpts{})
		wantErr(t, err, root+tc.want)
	}
}

// Merging leaves the files' trees as read: a snapshot merges them anew for
// each config it loads, and gets the same.
func TestMergeAgain(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "config.yaml")
	files(t, dir, map[string]string{
		"config.yaml": "include: a.yaml\nprofiles: {work: }\nmask: [a]\n",
		"a.yaml":      "profiles: {work: {model: m}}\nmask: [b]\n",
	})
	c, err := readChain(root, "")
	if err != nil {
		t.Fatal(err)
	}
	var first, second Config
	for _, cfg := range []*Config{&first, &second} {
		tr, err := mergeChain(c, ruleSets)
		if err != nil {
			t.Fatal(err)
		}
		if err := decodeTree(tr, cfg, decodeOpts{}); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(first, second) || !slices.Equal(first.Mask, []string{"a", "b"}) {
		t.Fatalf("first %+v, second %+v", first, second)
	}
	if c.files[0].tree.sub["profiles"].sub["work"].kind == c.files[1].tree.sub["profiles"].sub["work"].kind {
		t.Fatal("the null profile of config.yaml was merged into")
	}
	if len(c.files[0].tree.sub["mask"].items) != 1 {
		t.Fatal("the mask of config.yaml was merged into")
	}
}
