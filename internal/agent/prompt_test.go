package agent

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/GoldenDeals/aish/internal/session"
	"github.com/GoldenDeals/aish/internal/tools"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata/")

// hostPrompt is the host's system prompt as a request sends it from a
// shell of kind shell ("" is bash), with nothing added by tools, policy or
// config. The environment's lines but the shell's and the model's are
// given as "- KEY: …": the machine's facts (OS, host, user) are no part of
// the text under test.
func hostPrompt(t *testing.T, shell string) string {
	t.Helper()
	a, _, _, _, cwd := newAgent(t, nil)
	a.exec = tools.Exec{Dir: cwd, Shell: shell}
	a.Cfg.SystemPrompt = ""
	a.Cfg.Model, a.Cfg.Provider = "test-model", "anthropic"
	sys := a.request(nil).System
	head, env, ok := strings.Cut(sys, "\n\n# Environment\n")
	if !ok {
		t.Fatalf("no environment in the system prompt:\n%s", sys)
	}
	env, rest, more := strings.Cut(env, "\n\n")
	lines := strings.Split(env, "\n")
	for i, l := range lines {
		if k, _, ok := strings.Cut(l, ": "); ok && !strings.HasPrefix(l, "- Shell: ") && !strings.HasPrefix(l, "- Model: ") {
			lines[i] = k + ": …"
		}
	}
	out := head + "\n\n# Environment\n" + strings.Join(lines, "\n")
	if more {
		out += "\n\n" + rest
	}
	return out
}

// The host's system prompt, byte for byte: any change of its text shows in
// the diff of testdata/prompt. go test -run TestHostPromptGolden -update
// rewrites the files.
func TestHostPromptGolden(t *testing.T) {
	for _, c := range []struct{ shell, file string }{{"", "host-bash.golden"}, {"zsh", "host-zsh.golden"}} {
		got := hostPrompt(t, c.shell)
		path := filepath.Join("testdata", "prompt", c.file)
		if *update {
			if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if want := string(b); got != want {
			gl, wl := strings.Split(got, "\n"), strings.Split(want, "\n")
			i := 0
			for i < len(gl) && i < len(wl) && gl[i] == wl[i] {
				i++
			}
			line := func(ls []string) string {
				if i < len(ls) {
					return ls[i]
				}
				return "(end)"
			}
			t.Errorf("%s: line %d is\n%s\nwant\n%s\n(-update rewrites the file)", path, i+1, line(gl), line(wl))
		}
	}
}

// The rules the live shell needs, and the inputs the model must know, stay
// in the host's prompt through any rewrite of its text; what is no longer
// true stays out; the text stays short.
func TestHostPromptRules(t *testing.T) {
	for _, shell := range []string{"", "zsh"} {
		sys := hostPrompt(t, shell)
		for _, want := range []string{
			"Never run exit, exec, logout or return",
			"set -e",
			"`command NAME`",
			"stdin from /dev/null",
			"`" + hide("ghp_0123456789abcdefghij") + "`", // a token as Masker leaves it
			session.NotRecorded,
			"<system-reminder>",
			ByUser.Why,
			strings.TrimSuffix(overTime(time.Minute).Why, span(time.Minute)),
			"IMPORTANT: Assist with authorized security testing",
		} {
			if !strings.Contains(sys, want) {
				t.Errorf("shell %q: no %q in the system prompt", shell, want)
			}
		}
		if !hasCdRule(sys) {
			t.Errorf("shell %q: no rule against cd'ing into the cwd", shell)
		}
		if strings.Contains(sys, "Rego") {
			t.Errorf("shell %q: the system prompt names Rego", shell)
		}
		low := strings.ToLower(sys)
		for _, bad := range []string{"bypass", "nobody approves", "claude code", "yolo"} {
			if strings.Contains(low, bad) {
				t.Errorf("shell %q: %q in the system prompt", shell, bad)
			}
		}
		if n := len(sys) / 4; n > 3600 {
			t.Errorf("shell %q: the system prompt is %d tokens or so, over 3600", shell, n)
		}
	}
}

// Sections and files go together: one without the other, or a section
// listed twice, is no prompt to build.
func TestLoadSections(t *testing.T) {
	fsys := fstest.MapFS{
		"prompt/a.md": {Data: []byte("A\n\n")},
		"prompt/b.md": {Data: []byte("B")},
	}
	a, b := promptSection{name: "a"}, promptSection{name: "b"}
	text := loadSections(fsys, []promptSection{a}, []promptSection{b, a})
	if text["a"] != "A" || text["b"] != "B" {
		t.Errorf("sections %q", text)
	}
	for name, lists := range map[string][][]promptSection{
		"a file of no section":     {{a}},
		"a section without a file": {{a, b, {name: "c"}}},
		"a section listed twice":   {{a, b, a}},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: no panic", name)
				}
			}()
			loadSections(fsys, lists...)
		}()
	}
}
