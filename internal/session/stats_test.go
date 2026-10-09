package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// writeJournal puts es in dir as the journal of session id, with a line
// that is no JSON before them.
func writeJournal(t *testing.T, dir, id string, es ...Entry) string {
	t.Helper()
	b := []byte("{\"kind\":\"user\",\"text\":\"cut sh\n")
	for _, e := range es {
		line, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		b = append(append(b, line...), '\n')
	}
	path := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func turn(at time.Time, model string, in, cached, out int) Entry {
	return Entry{Kind: KindAssistant, Time: at, Provider: "p", Model: model, InputTokens: in, CachedTokens: cached, OutputTokens: out}
}

func subTurn(at time.Time, model string, in, cached, out int) Entry {
	return Entry{Kind: KindUsage, Time: at, About: "reviewer", Provider: "p", Model: model,
		Usage: &Usage{Input: in, Cached: cached, Output: out}}
}

// Sessions with entries of different times and models: what each period
// and each model spent. A request counts for the model of the turn that
// answers it, a session where it has a request or a turn; the agent's
// turns and its subagents' count alike; what is older than 90 days, and a
// session with commands alone, does not count.
func TestCollectStats(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	const day = 24 * time.Hour
	big := strings.Repeat("x", 200<<10) // past the reader's buffer
	writeJournal(t, dir, "20261001-100000-1",
		Entry{Kind: KindShell, Time: ago(2 * time.Hour), Cmd: "make", Output: big},
		Entry{Kind: KindUser, Time: ago(time.Hour), Text: "build it"},
		Entry{Kind: KindAssistant, Time: ago(time.Hour), Provider: "p", Model: "m1", InputTokens: 1000, CachedTokens: 800, OutputTokens: 50,
			Raw: json.RawMessage(`"` + big + `"`), ToolCalls: []ToolCall{{ID: "c1", Name: "task", Args: json.RawMessage(`{}`)}}},
		subTurn(ago(time.Hour), "m2", 300, 0, 20),
		Entry{Kind: KindToolResult, Time: ago(time.Hour), ToolCallID: "c1", ToolName: "task", Output: big},
		turn(ago(time.Hour), "m1", 1200, 1000, 60),
		Entry{Kind: KindUser, Time: ago(3 * day), Text: "earlier"},
		turn(ago(3*day), "m1", 500, 0, 10),
	)
	writeJournal(t, dir, "20261001-100000-2",
		Entry{Kind: KindUser, Time: ago(100 * day), Text: "long ago"},
		turn(ago(100*day), "m1", 9999, 0, 9999),
		Entry{Kind: KindUser, Time: ago(20 * day), Text: "last month"},
		turn(ago(20*day), "m2", 2000, 0, 100),
	)
	// Commands alone: no session of the agent's.
	writeJournal(t, dir, "20261001-100000-3",
		Entry{Kind: KindShell, Time: ago(2 * time.Hour), Cmd: "ls"})
	// A request cut off before any turn: no model to count it for.
	writeJournal(t, dir, "20261001-100000-4",
		Entry{Kind: KindUser, Time: ago(60 * day), Text: "stopped"},
		Entry{Kind: KindAssistant, Time: ago(60 * day), Text: "half [interrupted by the user]"})
	// Untouched for longer than the longest period: not opened, whatever
	// it says.
	old := writeJournal(t, dir, "20261001-100000-5",
		Entry{Kind: KindUser, Time: ago(time.Hour), Text: "?"},
		turn(ago(time.Hour), "m1", 7777, 0, 7777))
	if err := os.Chtimes(old, ago(91*day), ago(91*day)); err != nil {
		t.Fatal(err)
	}
	// Not a session's journal.
	if err := os.WriteFile(filepath.Join(dir, "notes.old.jsonl"), []byte(`{"kind":"user","time":"`+now.Format(time.RFC3339)+`"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	st, err := CollectStats(dir, Periods, now)
	if err != nil {
		t.Fatal(err)
	}
	wantTotal := []Spend{
		{Requests: 1, Sessions: 1, Input: 2500, Cached: 1800, Output: 130},
		{Requests: 2, Sessions: 1, Input: 3000, Cached: 1800, Output: 140},
		{Requests: 3, Sessions: 2, Input: 5000, Cached: 1800, Output: 240},
		{Requests: 4, Sessions: 3, Input: 5000, Cached: 1800, Output: 240},
	}
	if !reflect.DeepEqual(st.Total, wantTotal) {
		t.Errorf("total\n%+v\nwant\n%+v", st.Total, wantTotal)
	}
	m1 := Spend{Requests: 2, Sessions: 1, Input: 2700, Cached: 1800, Output: 120}
	m2 := Spend{Sessions: 1, Input: 300, Output: 20}
	wantModels := []ModelSpend{
		{"m1", []Spend{{Requests: 1, Sessions: 1, Input: 2200, Cached: 1800, Output: 110}, m1, m1, m1}},
		{"m2", []Spend{m2, m2, {Requests: 1, Sessions: 2, Input: 2300, Output: 120}, {Requests: 1, Sessions: 2, Input: 2300, Output: 120}}},
	}
	if !reflect.DeepEqual(st.Models, wantModels) {
		t.Errorf("models\n%+v\nwant\n%+v", st.Models, wantModels)
	}
}

// A directory without sessions, or none at all, is all zeros.
func TestCollectStatsEmpty(t *testing.T) {
	for _, dir := range []string{t.TempDir(), filepath.Join(t.TempDir(), "none")} {
		st, err := CollectStats(dir, Periods, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if len(st.Total) != len(Periods) || st.Total[3] != (Spend{}) || len(st.Models) != 0 {
			t.Errorf("%s: %+v", dir, st)
		}
	}
}

// A journal that cannot be read is named, and the others are counted.
func TestCollectStatsUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads any file")
	}
	dir := t.TempDir()
	now := time.Now()
	writeJournal(t, dir, "20261001-100000-1", Entry{Kind: KindUser, Time: now, Text: "hi"}, turn(now, "m", 10, 0, 1))
	bad := writeJournal(t, dir, "20261001-100000-2", Entry{Kind: KindUser, Time: now, Text: "hi"})
	if err := os.Chmod(bad, 0); err != nil {
		t.Fatal(err)
	}
	st, err := CollectStats(dir, Periods, now)
	if err == nil || !strings.Contains(err.Error(), "20261001-100000-2.jsonl") {
		t.Errorf("error %v", err)
	}
	if st.Total[0].Requests != 1 || st.Total[0].Input != 10 {
		t.Errorf("total %+v", st.Total[0])
	}
}

// What a subagent's turn cost is no part of the context: Tokens counts
// nothing for it, nor takes its tokens for the measure of the context.
func TestTokensSkipUsage(t *testing.T) {
	now := time.Now()
	es := []Entry{
		{Kind: KindUser, Time: now, Text: "go"},
		turn(now, "m", 1000, 0, 50),
		{Kind: KindToolResult, Time: now, Output: strings.Repeat("y", 400)},
	}
	with := append(append([]Entry(nil), es...), subTurn(now, "m", 300, 0, 20), subTurn(now, "m", 300, 0, 20))
	if got, want := Tokens(with, 0, 500), Tokens(es, 0, 500); got != want {
		t.Errorf("Tokens %+v with usage, %+v without", got, want)
	}
	if n := EntryBytes(subTurn(now, "m", 300, 0, 20), 0); n != 0 {
		t.Errorf("EntryBytes of usage %d", n)
	}
}
