package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GoldenDeals/aish/internal/shellstate"
)

// listSessions and journalSize are the directory the List benchmarks read:
// about 2 GB in $TMPDIR while one runs.
const (
	listSessions = 1000
	journalSize  = 2 << 20
)

// manySessions puts n closed sessions in dir, each a journal of about size
// bytes of commands with their output, requests and replies, a state and a
// model's name, as a shell left them.
func manySessions(tb testing.TB, dir string, n, size int) {
	tb.Helper()
	output := strings.Repeat("drwxr-xr-x 2 user user 4096 Oct  9 12:00 some-directory-name\n", 60)
	vars := map[string]string{}
	for v := range 40 {
		vars[fmt.Sprintf("VAR%d", v)] = fmt.Sprintf("declare -x VAR%d=%q", v, strings.Repeat("value", 10))
	}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for s := range n {
		id := fmt.Sprintf("20260101-000000-%d", s)
		var buf bytes.Buffer
		line := func(e Entry) {
			b, _ := json.Marshal(e)
			buf.Write(append(b, '\n'))
		}
		for i := 0; buf.Len() < size; i++ {
			at := start.Add(time.Duration(i) * time.Second)
			switch i % 10 {
			case 0:
				line(Entry{Kind: KindUser, Time: at, Text: fmt.Sprintf("session %d: what is in directory %d?", s, i)})
			case 1:
				line(Entry{Kind: KindAssistant, Time: at, Text: "Let me look.", Model: "m"})
			default:
				line(Entry{Kind: KindShell, Time: at, Cmd: fmt.Sprintf("ls -l /srv/%d", i), Output: output, Cwd: "/srv"})
			}
		}
		if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), buf.Bytes(), 0o600); err != nil {
			tb.Fatal(err)
		}
		st := Saved{Shell: shellstate.State{Vars: vars, Cwd: fmt.Sprintf("/srv/%d", s)}, TopLevel: true, Model: "m"}
		if err := SaveState(dir, id, st); err != nil {
			tb.Fatal(err)
		}
		if err := os.WriteFile(titlePath(dir, id), []byte(fmt.Sprintf("Session %d\n", s)), 0o600); err != nil {
			tb.Fatal(err)
		}
	}
}

func benchList(b *testing.B, cold bool) {
	dir := b.TempDir()
	manySessions(b, dir, listSessions, journalSize)
	if _, err := List(dir); err != nil {
		b.Fatal(err)
	}
	for b.Loop() {
		if cold {
			b.StopTimer()
			found, _ := filepath.Glob(filepath.Join(dir, "*.info"))
			for _, f := range found {
				os.Remove(f)
			}
			b.StartTimer()
		}
		list, err := List(dir)
		if err != nil || len(list) != listSessions {
			b.Fatalf("%d sessions: %v", len(list), err)
		}
	}
}

// BenchmarkList is List of a directory it has read before, as `aish
// resume` finds it but for the sessions used since.
func BenchmarkList(b *testing.B) { benchList(b, false) }

// BenchmarkListCold is List of a directory no List has read: every journal
// is read as it was before there were summaries.
func BenchmarkListCold(b *testing.B) { benchList(b, true) }
