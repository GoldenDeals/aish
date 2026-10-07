package policy

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// writeDir puts the files into a fresh directory.
func writeDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func hintsOf(t *testing.T, dir string, rules Rules) []string {
	t.Helper()
	e, err := Load(context.Background(), dir, rules)
	if err != nil {
		t.Fatal(err)
	}
	return e.Hints()
}

// A permit and a forbid alike carry a hint, given in the order of the
// policies in the file, and of the files in the directory.
func TestCedarHints(t *testing.T) {
	dir := writeDir(t, map[string]string{
		"b.cedar": `@hint("from b")
forbid(principal, action == Action::"run", resource == Command::"b");
`,
		"a.cedar": `@hint("write only under ~/work")
permit(principal, action, resource);

@reason("no sudo")
@hint("  sudo is forbidden here: use doas  ")
forbid(principal, action == Action::"run", resource == Command::"sudo");

@reason("no hint")
forbid(principal, action == Action::"run", resource == Command::"x");
`,
	})
	want := []string{"write only under ~/work", "sudo is forbidden here: use doas", "from b"}
	if got := hintsOf(t, dir, Rules{}); !slices.Equal(got, want) {
		t.Errorf("hints %q, want %q", got, want)
	}
}

// Every directory of the list is a checker of its own, and their hints go
// in the order of the list.
func TestCedarHintsTwoDirs(t *testing.T) {
	z := writeDir(t, map[string]string{"a.cedar": permitAll + `@hint("user's") forbid(principal, action == Action::"run", resource == Command::"a");` + "\n"})
	a := writeDir(t, map[string]string{"a.cedar": permitAll + `@hint("project's") forbid(principal, action == Action::"run", resource == Command::"b");` + "\n"})
	want := []string{"user's", "project's"}
	if got := hintsOf(t, z+string(filepath.ListSeparator)+a, Rules{}); !slices.Equal(got, want) {
		t.Errorf("hints %q, want %q", got, want)
	}
}

// An empty hint is a load error naming the file and the line of its
// policy: the rule must not go without the text quietly.
func TestEmptyHintIsLoadError(t *testing.T) {
	for _, ann := range []string{`@hint("")`, `@hint("  ")`, `@hint`} {
		dir := writeDir(t, map[string]string{"h.cedar": permitAll + "\n" + ann + "\nforbid(principal, action, resource == Command::\"sudo\");\n"})
		_, err := Load(context.Background(), dir, Rules{})
		if err == nil {
			t.Errorf("%s loaded", ann)
			continue
		}
		if want := filepath.Join(dir, "h.cedar") + ":3"; !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "@hint") {
			t.Errorf("%s: %v, want %s named", ann, err, want)
		}
	}
}

// The hints of [policy] go before those of Cedar, in the order of deny,
// ask and write_outside_home; a text said twice is said once, and its
// lines stay as written.
func TestRulesHints(t *testing.T) {
	dir := writeDir(t, map[string]string{"a.cedar": permitAll +
		`@hint("never push; the user pushes himself") forbid(principal, action == Action::"run", resource == Command::"git") when { context.args.contains("push") };` + "\n" +
		`@hint("rm is trash here") forbid(principal, action == Action::"run", resource == Command::"rm");` + "\n"})
	rules := Rules{
		Deny:             []string{"sudo *", "rm -rf /", "git push*"},
		Ask:              []string{"apt install *", "sudo *"},
		WriteOutsideHome: Deny,
		Hints: map[string]string{
			"git push*":     "never push; the user pushes himself",
			"apt install *": "packages:\n  ask the user first",
			"sudo *":        " sudo is forbidden: use doas\n",
		},
		WriteOutsideHomeHint: "write only under $HOME",
	}
	want := []string{
		"sudo is forbidden: use doas",
		"never push; the user pushes himself",
		"packages:\n  ask the user first",
		"write only under $HOME",
		"rm is trash here",
	}
	if got := hintsOf(t, dir, rules); !slices.Equal(got, want) {
		t.Errorf("hints %q, want %q", got, want)
	}
	if got := hintsOf(t, t.TempDir(), Rules{Deny: []string{"sudo *"}}); len(got) != 0 {
		t.Errorf("rules without hints: %q", got)
	}
}

// A hint of no rule is refused, as config.toml's check refuses it: the
// policy package is not to trust its caller to have checked.
func TestRulesHintsCheck(t *testing.T) {
	for _, r := range []Rules{
		{Deny: []string{"sudo *"}, Hints: map[string]string{"sudo*": "x"}},
		{Deny: []string{"sudo *"}, Hints: map[string]string{"sudo *": " "}},
		{Deny: []string{"sudo *"}, WriteOutsideHomeHint: "x"},
		{Deny: []string{"sudo *"}, WriteOutsideHome: Allow, WriteOutsideHomeHint: "x"},
		{WriteOutsideHome: Ask, WriteOutsideHomeHint: "\n"},
	} {
		if _, err := Load(context.Background(), t.TempDir(), r); err == nil {
			t.Errorf("%+v loaded", r)
		}
	}
	r := Rules{Ask: []string{"a"}, Hints: map[string]string{"a": "x"}}
	if n := r.Len(); n != 1 {
		t.Errorf("Len %d: a hint is not a rule", n)
	}
}

func TestNilEngineHints(t *testing.T) {
	var e *Engine
	if got := e.Hints(); got != nil {
		t.Errorf("nil engine: %q", got)
	}
	if got := (&Engine{}).Hints(); got != nil {
		t.Errorf("empty engine: %q", got)
	}
}

// A subagent's engine is the host's: the same hints.
func TestSubagentHints(t *testing.T) {
	dir := writeDir(t, map[string]string{"a.cedar": permitAll + `@hint("reviewer only reads") forbid(principal, action == Action::"write", resource) when { context has agent && context.agent == "reviewer" };` + "\n"})
	e, err := Load(context.Background(), dir, Rules{Deny: []string{"sudo *"}, Hints: map[string]string{"sudo *": "no sudo"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"no sudo", "reviewer only reads"}
	if got := e.Subagent("reviewer").Hints(); !slices.Equal(got, want) || !slices.Equal(e.Hints(), want) {
		t.Errorf("hints %q, subagent's %q, want %q", e.Hints(), got, want)
	}
}

// A hint is no rule: the verdicts with the hints and without them are the
// same, reasons included.
func TestHintsKeepVerdicts(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	cedar := `permit(principal, action, resource);
@reason("no sudo")
forbid(principal, action == Action::"run", resource == Command::"sudo");
@ask("push?")
forbid(principal, action == Action::"run", resource == Command::"git") when { context.args.contains("push") };
`
	hinted := strings.NewReplacer(`@reason("no sudo")`, `@reason("no sudo") @hint("use doas")`,
		`@ask("push?")`, `@hint("the user pushes") @ask("push?")`).Replace(cedar)
	plain := Rules{Deny: []string{"rm -rf /"}, Ask: []string{"apt install *"}, WriteOutsideHome: Ask}
	hints := plain
	hints.Hints = map[string]string{"rm -rf /": "never", "apt install *": "ask first"}
	hints.WriteOutsideHomeHint = "stay home"
	without, err := Load(ctx, writeDir(t, map[string]string{"a.cedar": cedar}), plain)
	if err != nil {
		t.Fatal(err)
	}
	with, err := Load(ctx, writeDir(t, map[string]string{"a.cedar": hinted}), hints)
	if err != nil {
		t.Fatal(err)
	}
	if len(with.Hints()) != 5 || len(without.Hints()) != 0 {
		t.Fatalf("hints %q and %q", with.Hints(), without.Hints())
	}
	seen := map[string]bool{}
	for _, in := range []Input{
		callInput("bash", map[string]any{"command": "sudo ls"}, home),
		callInput("bash", map[string]any{"command": "git push origin"}, home),
		callInput("bash", map[string]any{"command": "rm -rf /"}, home),
		callInput("bash", map[string]any{"command": "apt install x"}, home),
		callInput("bash", map[string]any{"command": "ls > /etc/x"}, home),
		callInput("bash", map[string]any{"command": "ls"}, home),
		callInput("write_file", map[string]any{"path": "/etc/hosts"}, home),
		callInput("read_file", map[string]any{"path": "/etc/hosts"}, home),
	} {
		a, b := check(t, without, in), check(t, with, in)
		if a != b {
			t.Errorf("%s %v: %+v without hints, %+v with", in.Tool, in.Args, a, b)
		}
		seen[a.Action] = true
	}
	if !seen[Allow] || !seen[Ask] || !seen[Deny] {
		t.Errorf("verdicts %v: the calls must try all three", seen)
	}
}

// The hints are in the key of the cache: an edited hint, once applied,
// gives an engine with the new text.
func TestCacheKeysHints(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	var c Cache
	rules := func(hint string) Rules {
		return Rules{Deny: []string{"sudo *"}, Hints: map[string]string{"sudo *": hint}}
	}
	first, err := c.Engine(ctx, dir, rules("use doas"))
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := c.Engine(ctx, dir, rules("use doas")); again != first {
		t.Error("the same rules compiled again")
	}
	second, err := c.Engine(ctx, dir, rules("use run0"))
	if err != nil {
		t.Fatal(err)
	}
	if second == first || !slices.Equal(second.Hints(), []string{"use run0"}) {
		t.Errorf("another hint: the same engine, hints %q", second.Hints())
	}
	woh, err := c.Engine(ctx, dir, Rules{WriteOutsideHome: Deny, WriteOutsideHomeHint: "stay home"})
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := c.Engine(ctx, dir, Rules{WriteOutsideHome: Deny}); again == woh {
		t.Error("write_outside_home_hint is not in the key")
	}
}
