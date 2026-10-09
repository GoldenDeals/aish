package session

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// BenchmarkLatest is `aish --resume` in a directory of listSessions
// sessions, the newest of which another aish holds.
func BenchmarkLatest(b *testing.B) {
	dir := b.TempDir()
	manySessions(b, dir, listSessions, journalSize)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for s := range listSessions {
		at := start.Add(time.Duration(s) * time.Minute)
		if err := os.Chtimes(filepath.Join(dir, fmt.Sprintf("20260101-000000-%d.jsonl", s)), at, at); err != nil {
			b.Fatal(err)
		}
	}
	held, err := lock(dir, fmt.Sprintf("20260101-000000-%d", listSessions-1))
	if err != nil {
		b.Fatal(err)
	}
	defer unlock(held)
	want := fmt.Sprintf("20260101-000000-%d", listSessions-2)
	for b.Loop() {
		s, err := Latest(dir)
		if err != nil || s.ID != want {
			b.Fatalf("%v: %v, want %s", s, err, want)
		}
	}
}
