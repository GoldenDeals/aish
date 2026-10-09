package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/GoldenDeals/aish/internal/shellstate"
)

func TestNextStartsNewFile(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for range 3 {
		if err := s.Append(Entry{Kind: KindShell, Cmd: "ls"}); err != nil {
			t.Fatal(err)
		}
		ids[s.ID] = true
		next := s.Next() // within the same second
		s.Unlock()
		s = next
		if err := s.Save(); err != nil {
			t.Fatal(err)
		}
	}
	if len(ids) != 3 || ids[s.ID] {
		t.Fatalf("journals %v and %s, want 4 different", ids, s.ID)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if len(files) != 4 {
		t.Fatalf("files %v, want 4", files)
	}
	for _, f := range files {
		o, err := Open(f)
		if err != nil {
			t.Fatal(err)
		}
		want := 1
		if o.ID == s.ID {
			want = 0 // saved right after the last Next
		}
		if n := len(o.Entries()); n != want {
			t.Errorf("%s has %d entries, want %d", f, n, want)
		}
	}
}

func TestOpenCountsBadLines(t *testing.T) {
	f := filepath.Join(t.TempDir(), "x.jsonl")
	journal := `{"kind":"shell","cmd":"ls"}` + "\n\n" + `{"kind":"user","te` + "\n" + `{"kind":"user","text":"hi"}` + "\n"
	if err := os.WriteFile(f, []byte(journal), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(f)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(s.Entries()); n != 2 {
		t.Errorf("%d entries, want 2", n)
	}
	if n := s.BadLines(); n != 1 {
		t.Errorf("%d bad lines, want 1 (a blank line is not bad)", n)
	}
}

// byDefault is bytes in tokens at the default measure.
func byDefault(bytes int) int { return int(float64(bytes) / defaultPerToken) }

func TestTokens(t *testing.T) {
	es := []Entry{
		{Kind: KindShell, Cmd: "cat big", Output: string(make([]byte, 100000))},
		{Kind: KindAssistant, Text: "hi", InputTokens: 5000, OutputTokens: 100},
		{Kind: KindShell, Cmd: "ls", Output: string(make([]byte, 4000))},
	}
	// Overhead unknown: the measure is the default one.
	est := Tokens(es, 1000, 0)
	if want := 5100 + byDefault(2+1000+40); est.Tokens != want || !est.Measured || est.PerToken != defaultPerToken {
		t.Errorf("tokens %+v, want %d", est, want)
	}
	es = append(es, Entry{Kind: KindSummary, Text: string(make([]byte, 400))})
	if est := Tokens(es, 1000, 0); est.Tokens != byDefault(400+40) || est.Measured {
		t.Errorf("after a summary: %+v", est)
	}
	// Till a turn is measured, the system prompt and the tools count too.
	if est := Tokens(es, 1000, 3000); est.Tokens != int(float64(400+40+3000)/est.PerToken) || est.Measured {
		t.Errorf("after a summary, with the overhead: %+v", est)
	}
	if c := Current(es); len(c) != 1 || c[0].Kind != KindSummary {
		t.Errorf("current %+v", c)
	}
	// Nothing to send, nothing to count, overhead or not.
	if est := Tokens([]Entry{{Kind: KindShell, Cmd: "ls"}, {Kind: KindClear}}, 1000, 3000); est.Tokens != 0 {
		t.Errorf("a cleared context: %+v", est)
	}
}

// Reasoning the provider does not send back is paid for, not carried into
// the next request's context.
func TestTokensDropped(t *testing.T) {
	es := []Entry{
		{Kind: KindUser, Text: "q"},
		{Kind: KindAssistant, Text: "a", InputTokens: 5000, OutputTokens: 900, DroppedTokens: 800},
	}
	if est := Tokens(es, 1000, 0); est.Tokens != 5100 {
		t.Errorf("tokens %d, want 5000 sent and the 100 of the reply", est.Tokens)
	}
	// A count past the reply's drops the reply, no more.
	es[1].DroppedTokens = 2000
	if est := Tokens(es, 1000, 0); est.Tokens != 5000 {
		t.Errorf("tokens %d, want 5000", est.Tokens)
	}
}

// Only a turn of the model is a measure of the context: an entry of
// another kind with tokens, a subagent's spending say, is not, and the
// model is not sent it.
func TestTokensMeasuredTurn(t *testing.T) {
	es := []Entry{
		{Kind: KindUser, Text: "q"},
		{Kind: KindAssistant, Text: "a", InputTokens: 5000, OutputTokens: 100},
		{Kind: KindToolResult, Output: "r", InputTokens: 90000, OutputTokens: 9000},
		{Kind: "spent", InputTokens: 70000, OutputTokens: 7000},
	}
	est := Tokens(es, 1000, 0)
	if want := 5100 + byDefault(1+40); est.Tokens != want || !est.Measured {
		t.Errorf("tokens %+v, want %d", est, want)
	}
}

// The bytes a token takes are measured within requests: from one turn the
// API counted to the next, the input grew by the reply and the results
// between them.
func TestTokensPerToken(t *testing.T) {
	out := strings.Repeat("x", 20000)
	turn := func(in, out int) Entry {
		return Entry{Kind: KindAssistant, Text: "t", Provider: "p", Model: "m", InputTokens: in, OutputTokens: out}
	}
	es := []Entry{
		{Kind: KindUser, Text: "list"},
		turn(3000, 500),
		{Kind: KindToolResult, Output: out},       // 20040 bytes
		turn(3000+500+10020, 200),                 // 10020 tokens for them: 2 bytes a token
		{Kind: KindShell, Cmd: "ls", Output: out}, // after it: 2+1000+40 bytes
	}
	est := Tokens(es, 1000, 0)
	if est.PerToken != 2 {
		t.Fatalf("%v bytes a token, want 2", est.PerToken)
	}
	if want := 13520 + 200 + (2+1000+40)/2; est.Tokens != want {
		t.Errorf("tokens %d, want %d", est.Tokens, want)
	}

	// No step across requests: some models drop the reasoning of the last
	// one unsaid. A step past belief is left out too: tools tool_search
	// loaded, say.
	more := append(slices.Clone(es[:4]),
		Entry{Kind: KindUser, Text: "again"},
		turn(50_000, 100),
		Entry{Kind: KindToolResult, Output: "loaded: a, b"},
		turn(60_000, 100),
	)
	if est := Tokens(more, 1000, 0); est.PerToken != 2 {
		t.Errorf("%v bytes a token, want still 2", est.PerToken)
	}
	// Nor a step that changed what goes before the conversation, however
	// likely its measure.
	loaded := append(slices.Clone(es[:4]), Entry{Kind: KindToolResult, Output: out}, turn(13520+200+5010, 100))
	loaded[5].Prefix = "tools loaded"
	if est := Tokens(loaded, 1000, 0); est.PerToken != 2 {
		t.Errorf("%v bytes a token past a new prefix, want still 2", est.PerToken)
	}
	// Reasoning the provider said it dropped is not in the next input.
	dropped := slices.Clone(es)
	dropped[1].OutputTokens, dropped[1].DroppedTokens = 2500, 2000
	if est := Tokens(dropped, 1000, 0); est.PerToken != 2 {
		t.Errorf("%v bytes a token with the reasoning dropped, want 2", est.PerToken)
	}

	// A summary since: the context is estimated whole, by the same measure.
	es = append(es, Entry{Kind: KindSummary, Text: strings.Repeat("s", 960)})
	if est := Tokens(es, 1000, 3000); est.PerToken != 2 || est.Tokens != (1000+3000)/2 || est.Measured {
		t.Errorf("after a summary: %+v", est)
	}

	// Another model's steps do not measure this one's tokenizer.
	other := append(slices.Clone(es[:4]), Entry{Kind: KindUser, Text: "q"},
		Entry{Kind: KindAssistant, Text: "t", Provider: "p", Model: "n", InputTokens: 15000})
	if est := Tokens(other, 1000, 0); est.PerToken != defaultPerToken {
		t.Errorf("%v bytes a token of another model", est.PerToken)
	}
	// Too little counted to tell.
	small := []Entry{{Kind: KindUser, Text: "hi"}, turn(3000, 10), {Kind: KindToolResult, Output: "ok"}, turn(3050, 10)}
	if est := Tokens(small, 1000, 0); est.PerToken != defaultPerToken {
		t.Errorf("%v bytes a token from 40 tokens", est.PerToken)
	}
}

func TestEntryBytes(t *testing.T) {
	for _, c := range []struct {
		name string
		e    Entry
		want int
	}{
		// Shell output counts as far as the model is sent it.
		{"long output", Entry{Kind: KindShell, Cmd: "cat big", Output: strings.Repeat("x", 5000)}, 7 + 1000 + 40},
		// A full-screen program's output is a screen, not truncated.
		{"tui", Entry{Kind: KindShell, Cmd: "top", Output: strings.Repeat("x", 5000), TUI: true}, 3 + 5000 + 40},
		{"assistant", Entry{Kind: KindAssistant, Text: "let me look", ToolCalls: []ToolCall{
			{ID: "1", Name: "bash", Args: json.RawMessage(`{"command":"ls"}`)},
			{ID: "2", Name: "read_file", Args: json.RawMessage(`{"path":"a"}`)},
		}}, 11 + 40 + 16 + 12},
		// A tool's result is not shell output: whole.
		{"tool result", Entry{Kind: KindToolResult, Output: strings.Repeat("x", 5000)}, 5000 + 40},
		// What the model is not sent weighs nothing.
		{"clear", Entry{Kind: KindClear}, 0},
		{"a kind for the journal alone", Entry{Kind: "spent", Text: "x"}, 0},
	} {
		if got := EntryBytes(c.e, 1000); got != c.want {
			t.Errorf("%s: %d bytes, want %d", c.name, got, c.want)
		}
	}
	if got := EntryBytes(Entry{Kind: KindShell, Output: strings.Repeat("x", 5000)}, 0); got != 5040 {
		t.Errorf("without max_output: %d bytes", got)
	}
}

func TestLockListFind(t *testing.T) {
	dir := t.TempDir()
	a, _ := New(dir)
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	a.Append(Entry{Kind: KindUser, Text: "first"}, Entry{Kind: KindUser, Text: "deploy it"})
	if err := SaveState(dir, a.ID, Saved{Model: "m", Shell: shellstate.State{Cwd: "/srv"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := lock(dir, a.ID); err == nil {
		t.Fatal("a locked session locked twice")
	}
	if err := Rename(dir, a.ID, "deploy"); err == nil {
		t.Fatal("renamed a session another holds")
	}
	if err := a.SetName("  deploy  "); err != nil {
		t.Fatal(err)
	}

	b := &Session{ID: "20200101-000000-1", path: filepath.Join(dir, "20200101-000000-1.jsonl"), saved: true}
	b.Append(Entry{Kind: KindShell, Cmd: "ls"})
	old := time.Now().Add(-time.Hour)
	os.Chtimes(b.path, old, old)
	if err := Rename(dir, b.ID, "deploy"); err == nil {
		t.Error("two sessions with one name")
	}

	list, err := List(dir)
	if err != nil || len(list) != 2 {
		t.Fatalf("%v %+v", err, list)
	}
	got := list[0]
	if got.ID != a.ID || got.Name != "deploy" || !got.Open || got.Last != "deploy it" || got.Requests != 2 || got.Cwd != "/srv" || got.Model != "m" {
		t.Errorf("%+v", got)
	}
	if list[1].Open {
		t.Error("b is not open")
	}
	for q, want := range map[string]string{"deploy": a.ID, "Dep": a.ID, "2020": b.ID, b.ID: b.ID} {
		if i, err := Find(list, q); err != nil || i.ID != want {
			t.Errorf("find %q: %v %v", q, i.ID, err)
		}
	}
	if _, err := Find(list, "nope"); err == nil {
		t.Error("found nothing")
	}

	// The session after a is not locked till it is on disk.
	next := a.Next()
	a.Unlock()
	if isOpen(dir, a.ID) || isOpen(dir, next.ID) {
		t.Error("a journal stayed locked after Unlock, or one not on disk is")
	}
	if err := next.Append(Entry{Kind: KindShell, Cmd: "ls"}); err != nil || !isOpen(dir, next.ID) {
		t.Errorf("on disk, yet not locked: %v", err)
	}
	next.Unlock()
	if isOpen(dir, next.ID) {
		t.Error("still locked")
	}
}
