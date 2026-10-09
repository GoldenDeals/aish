package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/session"
	"github.com/GoldenDeals/aish/internal/tools"
)

func TestColdCache(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	user := session.Entry{Kind: session.KindUser, Text: "q", Time: ago(time.Hour)}
	turn := func(in, cached int, at time.Time) session.Entry {
		return session.Entry{Kind: session.KindAssistant, Text: "a", InputTokens: in, CachedTokens: cached, Time: at}
	}
	sent := func(e session.Entry, provider, model, profile, prefix string) session.Entry {
		e.Provider, e.Model, e.Profile, e.Prefix = provider, model, profile, prefix
		return e
	}
	const ttl, min = 5 * time.Minute, 50000
	for _, tc := range []struct {
		name   string
		es     []session.Entry
		tokens int
		ttl    time.Duration
		min    int
		want   string
	}{
		{"no measured turn", []session.Entry{user, turn(0, 0, ago(time.Hour))}, 90000, ttl, min, ""},
		{"empty journal", nil, 90000, ttl, min, ""},
		{"fresh turn", []session.Entry{user, turn(90000, 80000, ago(time.Minute))}, 90000, ttl, min, ""},
		{"expired", []session.Entry{user, turn(90000, 80000, ago(23*time.Minute))}, 90000, ttl, min, "expired"},
		{"expired, small", []session.Entry{user, turn(9000, 0, ago(23*time.Minute))}, 9000, ttl, min, ""},
		{"expired, small by the estimate", []session.Entry{user, turn(90000, 0, ago(23*time.Minute))}, min - 1, ttl, min, ""},
		{"warning off", []session.Entry{user, turn(90000, 0, ago(23*time.Minute))}, 90000, ttl, 0, ""},
		{"ttl 0 checks no expiry", []session.Entry{user, turn(90000, 80000, ago(72*time.Hour))}, 90000, 0, min, ""},
		{"turn without a time", []session.Entry{user, turn(90000, 0, time.Time{})}, 90000, ttl, min, ""},
		{"uncached", []session.Entry{user, turn(60000, 0, ago(3*time.Minute)), turn(90000, 0, ago(time.Minute))}, 90000, ttl, min, "uncached"},
		{"uncached, ttl 0", []session.Entry{user, turn(60000, 0, ago(3*time.Hour)), turn(90000, 0, ago(time.Hour))}, 90000, 0, min, "uncached"},
		{"read from the cache", []session.Entry{user, turn(60000, 0, ago(3*time.Minute)), turn(90000, 60000, ago(time.Minute))}, 90000, ttl, min, ""},
		{"turns farther apart than ttl", []session.Entry{user, turn(60000, 0, ago(20*time.Minute)), turn(90000, 0, ago(time.Minute))}, 90000, ttl, min, ""},
		{"previous turn small", []session.Entry{user, turn(9000, 0, ago(3*time.Minute)), turn(90000, 0, ago(time.Minute))}, 90000, ttl, min, ""},
		{"last turn small", []session.Entry{user, turn(60000, 0, ago(3*time.Minute)), turn(9000, 0, ago(time.Minute)), user}, 90000, ttl, min, ""},
		{"unmeasured turn between", []session.Entry{user, turn(60000, 0, ago(3*time.Minute)), turn(0, 0, ago(2*time.Minute)), turn(90000, 0, ago(time.Minute))}, 90000, ttl, min, "uncached"},
		{"both: expired", []session.Entry{user, turn(60000, 0, ago(9*time.Minute)), turn(90000, 0, ago(7*time.Minute))}, 90000, ttl, min, "expired"},
		{"before a summary", []session.Entry{user, turn(90000, 0, ago(time.Hour)), {Kind: session.KindSummary, Text: "s", Time: ago(time.Minute)}, user}, 90000, ttl, min, ""},
		{"before a clear", []session.Entry{user, turn(90000, 0, ago(time.Hour)), {Kind: session.KindClear, Time: ago(time.Minute)}, user}, 90000, ttl, min, ""},
		{"previous before a summary", []session.Entry{user, turn(60000, 0, ago(3*time.Minute)), {Kind: session.KindSummary, Text: "s"}, user, turn(90000, 0, ago(time.Minute))}, 90000, ttl, min, ""},
		{"same prefix", []session.Entry{user, sent(turn(60000, 0, ago(3*time.Minute)), "anthropic", "m", "work", "p1"),
			sent(turn(90000, 0, ago(time.Minute)), "anthropic", "m", "work", "p1")}, 90000, ttl, min, "uncached"},
		{"another prefix", []session.Entry{user, sent(turn(60000, 0, ago(3*time.Minute)), "anthropic", "m", "work", "p1"),
			sent(turn(90000, 0, ago(time.Minute)), "anthropic", "m", "work", "p2")}, 90000, ttl, min, ""},
		{"another model", []session.Entry{user, sent(turn(60000, 0, ago(3*time.Minute)), "anthropic", "m", "work", "p1"),
			sent(turn(90000, 0, ago(time.Minute)), "anthropic", "n", "work", "p1")}, 90000, ttl, min, ""},
		{"another provider", []session.Entry{user, sent(turn(60000, 0, ago(3*time.Minute)), "anthropic", "m", "work", "p1"),
			sent(turn(90000, 0, ago(time.Minute)), "openai", "m", "work", "p1")}, 90000, ttl, min, ""},
		{"another profile", []session.Entry{user, sent(turn(60000, 0, ago(3*time.Minute)), "anthropic", "m", "work", "p1"),
			sent(turn(90000, 0, ago(time.Minute)), "anthropic", "m", "", "p1")}, 90000, ttl, min, ""},
		{"previous turn without a prefix", []session.Entry{user, turn(60000, 0, ago(3*time.Minute)),
			sent(turn(90000, 0, ago(time.Minute)), "", "", "", "p1")}, 90000, ttl, min, ""},
		{"another prefix, expired", []session.Entry{user, sent(turn(60000, 0, ago(9*time.Minute)), "anthropic", "m", "work", "p1"),
			sent(turn(90000, 0, ago(7*time.Minute)), "anthropic", "m", "work", "p2")}, 90000, ttl, min, "expired"},
	} {
		if got := coldCache(tc.es, tc.tokens, tc.ttl, tc.min, now); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestAge(t *testing.T) {
	for d, want := range map[time.Duration]string{
		2 * time.Second:                       "2s",
		59*time.Second + 600*time.Millisecond: "1m",
		23*time.Minute + 10*time.Second:       "23m",
		59*time.Minute + 40*time.Second:       "1h",
		5*time.Hour + 20*time.Minute:          "5h",
		23*time.Hour + 40*time.Minute:         "1d",
		3*24*time.Hour + 5*time.Hour:          "3d",
	} {
		if got := age(d); got != want {
			t.Errorf("age(%v) = %q, want %q", d, got, want)
		}
	}
}

// coldJournal is a session whose last turns were measured: a request,
// then a turn of 60k tokens at first and one of 90k at last.
func coldJournal(first, last time.Time, cached int) []session.Entry {
	return []session.Entry{
		{Kind: session.KindUser, Text: "look around", Cwd: "/", Time: first.Add(-time.Second)},
		{Kind: session.KindAssistant, Text: "first", InputTokens: 60000, OutputTokens: 100, Time: first},
		{Kind: session.KindAssistant, Text: "done", InputTokens: 90000, CachedTokens: cached, OutputTokens: 100, Time: last},
	}
}

// After a pause past cache_ttl the request is told it pays for the whole
// session again, and goes on all the same.
func TestStartWarnsExpiredCache(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{{Text: "here it is"}}}
	a, j, _, ui, cwd := newAgent(t, prov)
	now := time.Now()
	j.es = coldJournal(now.Add(-25*time.Minute), now.Add(-23*time.Minute), 60000)
	if err := a.Start(context.Background(), "and now?", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	out := ui.String()
	if !strings.Contains(out, "[aish: provider cache expired (last turn 23m ago); this session is ~90k tokens") {
		t.Errorf("no warning:\n%s", out)
	}
	if len(prov.requests) != 1 || !strings.Contains(out, "here it is") {
		t.Errorf("the request did not go on: %d turns\n%s", len(prov.requests), out)
	}
	if i := strings.Index(out, "provider cache expired"); i > strings.Index(out, "here it is") {
		t.Errorf("the warning came after the turn:\n%s", out)
	}
}

// A cache that is not read is told of once a session: the second request
// in the same state hears nothing, a request in another session does.
func TestStartWarnsUncachedOnce(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{{Text: "one"}, {Text: "two"}, {Text: "three"}}}
	a, j, _, ui, cwd := newAgent(t, prov)
	now := time.Now()
	j.es = coldJournal(now.Add(-2*time.Minute), now.Add(-time.Minute), 0)
	const line = "[aish: the provider did not read this session from its cache on the last turn (~90k tokens); every request pays in full."
	if err := a.Start(context.Background(), "more", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ui.String(), line) {
		t.Fatalf("no warning:\n%s", ui.String())
	}
	ui.Reset()
	if err := a.Start(context.Background(), "again", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ui.String(), "[aish: the provider did not read") {
		t.Errorf("told twice:\n%s", ui.String())
	}
	if len(prov.requests) != 2 {
		t.Errorf("%d turns, want 2", len(prov.requests))
	}
	// aish resume: another journal, told of again.
	ui.Reset()
	j.id = "s2"
	if err := a.Start(context.Background(), "elsewhere", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ui.String(), line) {
		t.Errorf("another session not told:\n%s", ui.String())
	}
}

// A subagent's journal is new: nothing measured, nothing to warn of, how
// low the threshold may be.
func TestStartNoWarningOnNewJournal(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{{Text: "fine", InputTokens: 90000}}}
	a, _, _, ui, cwd := newAgent(t, prov)
	a.Cfg.ColdWarnTokens, a.Cfg.CacheTTL = 1, "1s"
	if err := a.Start(context.Background(), "check", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ui.String(), "[aish: provider cache") || strings.Contains(ui.String(), "[aish: the provider did not") {
		t.Errorf("warned on a new journal:\n%s", ui.String())
	}
}

// A turn records the prefix of its request: the same while the system
// prompt, the tools and what the messages are made by stay, another when
// any of them changes.
func TestPrefixKey(t *testing.T) {
	a, _, _, _, cwd := newAgent(t, &fakeProvider{})
	a.exec = tools.Exec{Dir: cwd}
	key := func() string {
		t.Helper()
		a.env = ""
		return a.prefixKey(a.request([]session.Entry{{Kind: session.KindUser, Text: "q", Cwd: cwd}}))
	}
	base := key()
	if base == "" || key() != base {
		t.Fatalf("not stable: %q, %q", base, key())
	}
	if got := a.prefixKey(a.request([]session.Entry{{Kind: session.KindUser, Text: "another", Cwd: cwd}})); got != base {
		t.Errorf("the messages changed the prefix: %q, %q", got, base)
	}
	for _, tc := range []struct {
		name   string
		change func()
		undo   func()
	}{
		{"system_prompt", func() { a.Cfg.SystemPrompt = "Be brief." }, func() { a.Cfg.SystemPrompt = "" }},
		{"effort", func() { a.Cfg.Effort = "high" }, func() { a.Cfg.Effort = "" }},
		{"max_output_bytes", func() { a.Cfg.MaxOutputBytes++ }, func() { a.Cfg.MaxOutputBytes-- }},
		{"mask", func() { a.Cfg.Mask = []string{"tok_[a-z]+"} }, func() { a.Cfg.Mask = nil }},
		{"tools", func() { a.Tools = tools.Load(""); a.Tools.Add(&taskTool{a: a}) }, func() { a.Tools = tools.Load("") }},
	} {
		tc.change()
		if got := key(); got == base {
			t.Errorf("%s: the prefix stayed", tc.name)
		}
		tc.undo()
		if got := key(); got != base {
			t.Errorf("%s undone: %q, want %q", tc.name, got, base)
		}
	}
}

// The turn after a change of the prefix reads nothing, as it should: the
// next request is not told of it; the one after a turn with the same
// prefix that read nothing is.
func TestStartColdAfterPrefixChange(t *testing.T) {
	turn := func(in, cached int) *llm.Response {
		return &llm.Response{Text: "ok", InputTokens: in, CachedTokens: cached}
	}
	prov := &fakeProvider{replies: []*llm.Response{turn(60000, 0), turn(70000, 60000), turn(80000, 0), turn(90000, 0), turn(95000, 0)}}
	a, j, _, ui, cwd := newAgent(t, prov)
	start := func() {
		t.Helper()
		if err := a.Start(context.Background(), "more", tools.Exec{Dir: cwd}); err != nil {
			t.Fatal(err)
		}
	}
	const line = "[aish: the provider did not read this session from its cache"
	start()
	start()
	a.Cfg.SystemPrompt = "Be thorough."
	start()
	start()
	if strings.Contains(ui.String(), line) {
		t.Fatalf("told of a turn that wrote the cache anew:\n%s", ui.String())
	}
	var prefixes []string
	for _, e := range j.es {
		if e.Kind == session.KindAssistant {
			prefixes = append(prefixes, e.Prefix)
		}
	}
	if len(prefixes) != 4 || prefixes[0] != prefixes[1] || prefixes[1] == prefixes[2] || prefixes[2] != prefixes[3] {
		t.Fatalf("prefixes %q", prefixes)
	}
	start()
	if !strings.Contains(ui.String(), line) {
		t.Errorf("a cache not read with the same prefix is not told of:\n%s", ui.String())
	}
}
