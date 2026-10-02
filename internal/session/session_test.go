package session

import (
	"path/filepath"
	"testing"
)

func TestClearStartsNewFile(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for range 3 {
		if err := s.Append(Entry{Kind: KindShell, Cmd: "ls"}); err != nil {
			t.Fatal(err)
		}
		ids[s.ID] = true
		s.Clear() // within the same second
	}
	if len(ids) != 3 {
		t.Fatalf("journals %v, want 3 different", ids)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	for _, f := range files {
		o, err := Open(f)
		if err != nil {
			t.Fatal(err)
		}
		if n := len(o.Entries()); n != 1 {
			t.Errorf("%s has %d entries, want 1", f, n)
		}
	}
}

func TestTokens(t *testing.T) {
	es := []Entry{
		{Kind: KindShell, Cmd: "cat big", Output: string(make([]byte, 100000))},
		{Kind: KindAssistant, Text: "hi", InputTokens: 5000, OutputTokens: 100},
		{Kind: KindShell, Cmd: "ls", Output: string(make([]byte, 4000))},
	}
	if got := Tokens(es, 1000); got != 5100+(2+1000+40)/4 {
		t.Errorf("tokens %d", got)
	}
	es = append(es, Entry{Kind: KindSummary, Text: string(make([]byte, 400))})
	if got := Tokens(es, 1000); got != (400+40)/4 {
		t.Errorf("after a summary: %d", got)
	}
	if c := Current(es); len(c) != 1 || c[0].Kind != KindSummary {
		t.Errorf("current %+v", c)
	}
}
