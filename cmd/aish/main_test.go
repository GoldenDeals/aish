package main

import (
	"slices"
	"testing"
	"time"

	"github.com/inebotov/aish/internal/llm"
)

func TestFirstSentence(t *testing.T) {
	for in, want := range map[string]string{
		"Reads a file. You can access any file.": "Reads a file.",
		"Show the weather (wttr.in).\nUsage: …":  "Show the weather (wttr.in).",
		"One line, no period":                    "One line, no period",
		"Ends with a period.":                    "Ends with a period.",
		"Version 1.2 is out. More":               "Version 1.2 is out.",
		". Leading":                              ". Leading",
		"":                                       "",
	} {
		if got := firstSentence(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestTrimDashes(t *testing.T) {
	for _, tc := range []struct{ in, want []string }{
		{[]string{"--", "list", "files"}, []string{"list", "files"}},
		{[]string{"--", "--", "x"}, []string{"--", "x"}},
		{[]string{"list", "--"}, []string{"list", "--"}},
		{[]string{"--"}, []string{}},
		{nil, nil},
	} {
		if got := trimDashes(tc.in); !slices.Equal(got, tc.want) {
			t.Errorf("%q: %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseModelArgs(t *testing.T) {
	for _, tc := range []struct {
		provider string
		args     []string
		want     modelArgs
		err      string
	}{
		{"anthropic", nil, modelArgs{}, ""},
		{"anthropic", []string{"claude-x"}, modelArgs{name: "claude-x", setName: true}, ""},
		{"anthropic", []string{"high"}, modelArgs{effort: "high", setEffort: true}, ""},
		{"anthropic", []string{"default"}, modelArgs{setEffort: true}, ""},
		{"anthropic", []string{"claude-x", "max"}, modelArgs{name: "claude-x", effort: "max", setName: true, setEffort: true}, ""},
		{"anthropic", []string{"claude-x", "default"}, modelArgs{name: "claude-x", setName: true, setEffort: true}, ""},
		// Levels differ by provider: minimal is OpenAI's only.
		{"anthropic", []string{"minimal"}, modelArgs{name: "minimal", setName: true}, ""},
		{"openai", []string{"minimal"}, modelArgs{effort: "minimal", setEffort: true}, ""},
		{"anthropic", []string{"claude-x", "minimal"}, modelArgs{}, `no effort "minimal" for anthropic (want low, medium, high, xhigh, max or default)`},
		{"anthropic", []string{"a", "high", "b"}, modelArgs{}, "usage: aish model [NAME] [EFFORT|default]"},
	} {
		got, err := parseModelArgs(tc.provider, tc.args)
		if tc.err != "" {
			if err == nil || err.Error() != tc.err {
				t.Errorf("%s %q: error %v, want %q", tc.provider, tc.args, err, tc.err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%s %q: %+v, %v; want %+v", tc.provider, tc.args, got, err, tc.want)
		}
	}
}

func TestEffortLevels(t *testing.T) {
	m := llm.ModelInfo{Efforts: []string{"low", "high"}, EffortsKnown: true}
	if got := effortLevels(m, true, "high"); got != "effort low \x1b[0;1mhigh\x1b[0;2m" {
		t.Errorf("current: %q", got)
	}
	if got := effortLevels(m, false, "high"); got != "effort low high" {
		t.Errorf("other model: %q", got)
	}
	if m.Efforts[1] != "high" {
		t.Errorf("the model's levels changed: %q", m.Efforts)
	}
	if got := effortLevels(llm.ModelInfo{EffortsKnown: true}, false, ""); got != "no effort" {
		t.Errorf("no levels: %q", got)
	}
	if got := effortLevels(llm.ModelInfo{}, true, "high"); got != "" {
		t.Errorf("unknown levels: %q", got)
	}
	if effortName("") != "default" || effortName("low") != "low" {
		t.Error("effortName")
	}
}

func TestKeys(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"ab", []string{"a", "b"}},
		{"\x1b[A\x1b[B\r", []string{"\x1b[A", "\x1b[B", "\r"}},
		{"\x1bOH", []string{"\x1bOH"}},
		{"\x1b[1;5C", []string{"\x1b[1;5C"}},
		{"\x1b", []string{"\x1b"}},
		{"\x1bx", []string{"\x1b", "x"}},
		{"\x1b[", []string{"\x1b", "["}},
		// A sequence cut off at the end of the read.
		{"\x1b[12", []string{"\x1b[12"}},
		{"ёж", []string{"ё", "ж"}},
	} {
		if got := keys([]byte(tc.in)); !slices.Equal(got, tc.want) {
			t.Errorf("%q: %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestHome(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	for in, want := range map[string]string{
		"/home/u":              "~",
		"/home/u/.config/aish": "~/.config/aish",
		"/home/user/x":         "/home/user/x",
		"/etc/aish":            "/etc/aish",
	} {
		if got := home(in); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
	t.Setenv("HOME", "/")
	if got := home("/etc"); got != "/etc" {
		t.Errorf("home /: %s", got)
	}
}

func TestAgo(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{10 * time.Second, "just now"},
		{5 * time.Minute, "5m ago"},
		{3 * time.Hour, "3h ago"},
		{50 * time.Hour, "2d ago"},
	} {
		if got := ago(now.Add(-tc.d)); got != tc.want {
			t.Errorf("%v: %q, want %q", tc.d, got, tc.want)
		}
	}
	old := now.AddDate(0, -1, 0)
	if got := ago(old); got != old.Format("2 Jan 2006") {
		t.Errorf("a month ago: %q", got)
	}
}
