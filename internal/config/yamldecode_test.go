package config

import (
	"bytes"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"go.yaml.in/yaml/v3"
)

// A key no field takes is an error with its path and line, in a map of a
// map too; so is a value of another type, without the value.
func TestDecodeErrors(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "config.yaml")
	const secret = "sk-ant-secret123"
	for _, tc := range []struct{ data, want string }{
		{"profiles:\n  work:\n    model: x\n    modle: y\n", `:4: unknown key "profiles.work.modle"`},
		{"route:\n  capital: true\n  sufix: x\n", `:3: unknown key "route.sufix"`},
		{"policy: {hints: {a: b}, ask: [], foo: " + secret + "}\n", `:1: unknown key "policy.foo"`},
		{"\n\nmcp:\n  servers: {}\n", `:3: unknown key "mcp"`},
		{"x:\n  include: a.yaml\n", `:1: unknown key "x"`},
		{"route:\n  include: a.yaml\n", `:2: unknown key "route.include"`},
		{"max_steps: 1.5\n", ":1: max_steps: want a whole number"},
		{"max_steps: \"5\"\n", ":1: max_steps: want a whole number"},
		{"max_steps: 0x10\n", ":1: max_steps: want a whole number"},
		{"max_steps: 1_000\n", ":1: max_steps: want a whole number"},
		{"max_steps: 99999999999999999999\n", ":1: max_steps: want a whole number"},
		{"max_steps: " + secret + "\n", ":1: max_steps: want a whole number"},
		{"markdown: yes\n", ":1: markdown: want true or false"},
		{"markdown: on\n", ":1: markdown: want true or false"},
		{"markdown: \"true\"\n", ":1: markdown: want true or false"},
		{"markdown: " + secret + "\n", ":1: markdown: want true or false"},
		{"compact_at: half\n", ":1: compact_at: want a number"},
		{"compact_at: .inf\n", ":1: compact_at: want a number"},
		{"model: [" + secret + "]\n", ":1: model: want a string, not a list or a map"},
		{"model: {a: " + secret + "}\n", ":1: model: want a string, not a list or a map"},
		{"mask: " + secret + "\n", ":1: mask: want a list"},
		{"mask:\n  - [" + secret + "]\n", ":2: mask: want a list of strings"},
		{"route: " + secret + "\n", ":1: route: want a map of keys"},
		{"profiles: [" + secret + "]\n", ":1: profiles: want a map of keys"},
		{"profiles:\n  work: " + secret + "\n", ":2: profiles.work: want a map of keys"},
		{"profiles:\n  work:\n    max_tokens: " + secret + "\n", ":3: profiles.work.max_tokens: want a whole number"},
		{"http_proxy: {" + secret + ": x}\n", ":1: http_proxy: want a string, not a list or a map"},
	} {
		files(t, dir, map[string]string{"config.yaml": tc.data})
		cfg := Default()
		_, err := loadYAML(root, "", &cfg, decodeOpts{})
		wantErr(t, err, root+tc.want)
		if strings.Contains(err.Error(), "sk-ant") {
			t.Fatalf("the value in the error: %v", err)
		}
	}
}

// Integers are decimal, floats take them too, a bool is true or false, and
// a string is the text of the scalar, a number's too.
func TestDecodeScalars(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "config.yaml")
	files(t, dir, map[string]string{"config.yaml": `
model: 1.10
effort: no
code_style: 3
api_key: "*secret*"
max_steps: +12
max_output_bytes: 010
max_tokens: -0
compact_at: 0
fold_lines: -1
markdown: False
hide_work: TRUE
prompt_status: !!bool true
route:
  suffix: "?"
  min_words: 3
profiles:
  work:
    model: 3.5
    max_tokens: 9000
    context_window: 32000
    http_proxy: ""
`})
	cfg := Default()
	if _, err := loadYAML(root, "", &cfg, decodeOpts{}); err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "1.10" || cfg.Effort != "no" || cfg.CodeStyle != "3" || cfg.APIKey != "*secret*" {
		t.Fatalf("strings: %q %q %q %q", cfg.Model, cfg.Effort, cfg.CodeStyle, cfg.APIKey)
	}
	if cfg.MaxSteps != 12 || cfg.MaxOutputBytes != 10 || cfg.MaxTokens != 0 || cfg.CompactAt != 0 || cfg.FoldLines != -1 {
		t.Fatalf("numbers: %d %d %d %v %d", cfg.MaxSteps, cfg.MaxOutputBytes, cfg.MaxTokens, cfg.CompactAt, cfg.FoldLines)
	}
	if cfg.Markdown || !cfg.HideWork || !cfg.PromptStatus {
		t.Fatalf("bools: %v %v %v", cfg.Markdown, cfg.HideWork, cfg.PromptStatus)
	}
	if cfg.Route.Suffix != "?" || cfg.Route.MinWords != 3 || !cfg.Route.Capital {
		t.Fatalf("route %+v", cfg.Route)
	}
	w := cfg.Profiles["work"]
	if *w.Model != "3.5" || *w.MaxTokens != 9000 || *w.ContextWindow != 32000 || *w.HTTPProxy != "" || w.BaseURL != nil {
		t.Fatalf("profile %+v", w)
	}
	// Defaults stay where the file sets nothing.
	if cfg.CacheTTL != "5m" || cfg.ColdWarnTokens != 50000 {
		t.Fatalf("defaults: %q %d", cfg.CacheTTL, cfg.ColdWarnTokens)
	}

	files(t, dir, map[string]string{"config.yaml": "compact_at: .5\n"})
	if _, err := loadYAML(root, "", &cfg, decodeOpts{}); err != nil || cfg.CompactAt != 0.5 {
		t.Fatalf("compact_at %v: %v", cfg.CompactAt, err)
	}
	files(t, dir, map[string]string{"config.yaml": "compact_at: 1\n"})
	if _, err := loadYAML(root, "", &cfg, decodeOpts{}); err != nil || cfg.CompactAt != 1 {
		t.Fatalf("compact_at %v: %v", cfg.CompactAt, err)
	}
}

// A key of the user's config is not allowed in a project's, wherever in
// the chain; one of neither is unknown.
func TestDecodeProject(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, ".aish.yaml")
	files(t, dir, map[string]string{
		".aish.yaml":         "include: .aish/*.yaml\nmax_steps: 7\n",
		".aish/policy.yaml":  "policy_dir: policy\npolicy:\n  deny: [\"git push --force*\"]\n",
		".aish/prompts.yaml": "system_prompt: Go\n",
	})
	var pr project
	if _, err := loadYAML(root, dir, &pr, decodeOpts{user: Config{}}); err != nil {
		t.Fatal(err)
	}
	if *pr.MaxSteps != 7 || *pr.PolicyDir != "policy" || pr.Policy.Deny[0] != "git push --force*" || *pr.SystemPrompt != "Go" || pr.HooksDir != nil {
		t.Fatalf("project %+v", pr)
	}
	for _, tc := range []struct{ data, want string }{
		{"\nmodel: x\n", `:2: key "model" is not allowed in a project config: set it in ~/.config/aish/config.yaml`},
		{"profiles:\n  x: {}\n", `:1: key "profiles" is not allowed in a project config: set it in ~/.config/aish/config.yaml`},
		{"modle: x\n", `:1: unknown key "modle"`},
		{"policy:\n  model: x\n", `:2: unknown key "policy.model"`},
	} {
		files(t, dir, map[string]string{".aish/prompts.yaml": tc.data})
		_, err := loadYAML(root, dir, &project{}, decodeOpts{user: Config{}})
		wantErr(t, err, filepath.Join(dir, ".aish", "prompts.yaml")+tc.want)
	}
}

// A key of pathLists takes a path or a list of them, as PATH; another
// string key takes no list.
func TestDecodePathLists(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "config.yaml")
	o := decodeOpts{pathLists: []string{"policy_dir", "tools_dir"}}
	for _, tc := range []struct{ data, policy, tools string }{
		{"policy_dir: [a, ~/b, /c]\ntools_dir: t\n", "a:~/b:/c", "t"},
		{"policy_dir: []\ntools_dir: \"\"\n", "", ""},
	} {
		files(t, dir, map[string]string{"config.yaml": tc.data})
		var cfg Config
		if _, err := loadYAML(root, "", &cfg, o); err != nil {
			t.Fatal(err)
		}
		if cfg.PolicyDir != tc.policy || cfg.ToolsDir != tc.tools {
			t.Fatalf("%q: policy_dir %q, tools_dir %q", tc.data, cfg.PolicyDir, cfg.ToolsDir)
		}
	}
	for _, tc := range []struct{ data, want string }{
		{"policy_dir: a:b\n", `:1: policy_dir: a path with ":" in it: give several as a list`},
		{"policy_dir:\n  - a\n  - b:c\n", `:3: policy_dir: a path with ":" in it: give several as a list`},
		{"policy_dir: [a, \"\"]\n", ":1: policy_dir: an empty path in the list"},
		{"policy_dir: [a, [b]]\n", ":1: policy_dir: want a path or a list of paths"},
		{"policy_dir: {a: b}\n", ":1: policy_dir: want a path or a list of paths"},
		{"policy_dir:\n", `:1: policy_dir: use "" for an empty string`},
		{"hooks_dir: [a, b]\n", ":1: hooks_dir: want a string, not a list or a map"},
	} {
		files(t, dir, map[string]string{"config.yaml": tc.data})
		_, err := loadYAML(root, "", &Config{}, o)
		wantErr(t, err, root+tc.want)
	}
}

// An embedded struct lends its keys: a file's schema is Config and what is
// not in it, mcp: servers: say.
func TestDecodeEmbedded(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "config.yaml")
	files(t, dir, map[string]string{
		"config.yaml": "model: m\ninclude: mcp.yaml\nmcp:\n  servers:\n    gh:\n      env: {TOKEN: t}\n",
		"mcp.yaml":    "mcp:\n  servers:\n    gh:\n      command: npx\n      args: [-y, gh]\n      timeout: 120\n",
	})
	type server struct {
		Command string            `toml:"command"`
		Args    []string          `toml:"args"`
		Env     map[string]string `toml:"env"`
		Timeout int               `toml:"timeout"`
	}
	var file struct {
		Config
		MCP struct {
			Servers map[string]server `toml:"servers"`
		} `toml:"mcp"`
	}
	if _, err := loadYAML(root, "", &file, decodeOpts{}); err != nil {
		t.Fatal(err)
	}
	gh := file.MCP.Servers["gh"]
	if file.Model != "m" || gh.Command != "npx" || len(gh.Args) != 2 || gh.Env["TOKEN"] != "t" || gh.Timeout != 120 {
		t.Fatalf("decoded %+v", file)
	}
}

// Every key of Config and of a project, by its tag, is read from YAML as
// it is from TOML.
func TestDecodeLikeTOML(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeFor[Config](), reflect.TypeFor[project]()} {
		doc := sample(typ, "")
		var tb bytes.Buffer
		if err := toml.NewEncoder(&tb).Encode(doc); err != nil {
			t.Fatal(err)
		}
		fromTOML := reflect.New(typ)
		md, err := toml.Decode(tb.String(), fromTOML.Interface())
		if err != nil || len(md.Undecoded()) > 0 {
			t.Fatalf("%s: TOML: %v, undecoded %v", typ, err, md.Undecoded())
		}
		yb, err := yaml.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		root := filepath.Join(dir, "config.yaml")
		files(t, dir, map[string]string{"config.yaml": string(yb)})
		fromYAML := reflect.New(typ)
		tr, err := loadYAML(root, "", fromYAML.Interface(), decodeOpts{})
		if err != nil {
			t.Fatalf("%s: %v\n%s", typ, err, yb)
		}
		if !reflect.DeepEqual(fromYAML.Interface(), fromTOML.Interface()) {
			t.Fatalf("%s: from YAML\n%+v\nfrom TOML\n%+v", typ, fromYAML.Elem(), fromTOML.Elem())
		}
		if fromYAML.Elem().IsZero() {
			t.Fatalf("%s: nothing decoded", typ)
		}
		for _, k := range tagKeys(typ, "") {
			if tr.at(k) == "" {
				t.Errorf("%s: key %s is not in the sample", typ, k)
			}
		}
	}
}

// sample is a document setting every key of type t, by keyTag, to a value
// that is not the zero one: a map has two keys, each with every key of
// its values.
func sample(t reflect.Type, key string) any {
	switch t.Kind() {
	case reflect.Pointer:
		return sample(t.Elem(), key)
	case reflect.Struct:
		doc := map[string]any{}
		for i := range t.NumField() {
			f := t.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get(keyTag), ",")
			if name != "-" && f.IsExported() {
				doc[name] = sample(f.Type, name)
			}
		}
		return doc
	case reflect.Map:
		return map[string]any{"one": sample(t.Elem(), "one"), "two words": sample(t.Elem(), "two words")}
	case reflect.Slice:
		return []any{key + " a", key + " b"}
	case reflect.String:
		return key + " value"
	case reflect.Bool:
		return true
	case reflect.Int, reflect.Int64:
		return 7
	case reflect.Float64:
		return 0.25
	}
	panic("sample: no value for " + t.String())
}

// tagKeys are the paths of the keys of struct type t, its structs' keys
// too, as tomlKeys has them for the README.
func tagKeys(t reflect.Type, prefix string) []string {
	var keys []string
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get(keyTag), ",")
		if name == "-" || !f.IsExported() {
			continue
		}
		key := keyPath(prefix, name)
		keys = append(keys, key)
		ft := f.Type
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct {
			keys = append(keys, tagKeys(ft, key)...)
		}
	}
	return keys
}
