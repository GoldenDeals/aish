package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/session"
)

// spentSession puts in dir a session with a request an hour ago that a
// turn of model answered, at 1500 tokens in and 40 out.
func spentSession(t *testing.T, dir, id, model string) {
	t.Helper()
	at := time.Now().Add(-time.Hour)
	var b []byte
	for _, e := range []session.Entry{
		{Kind: session.KindUser, Time: at, Text: "hi"},
		{Kind: session.KindAssistant, Time: at, Provider: "p", Model: model, Text: "hello", InputTokens: 1500, OutputTokens: 40},
	} {
		line, _ := json.Marshal(e)
		b = append(append(b, line...), '\n')
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// aish stats counts the sessions of the proxy's directory inside aish,
// config.toml broken on disk or not, and those of sessions_dir outside.
func TestStatsCmd(t *testing.T) {
	disk := diskConfig(t, "max_steps = -1\n")
	sessions := t.TempDir() // the proxy's
	spentSession(t, sessions, "20261001-100000-1", "model-in-force")
	spentSession(t, disk.SessionsDir, "20261001-100000-2", "model-on-disk")
	fakeProxy(t, map[string]any{rpc.MethodInfo: rpc.Info{SessionID: "cur", Dir: sessions, Saved: true}})

	code, stdout, stderr := captured(t, func() int { return run([]string{"stats"}) })
	if code != 0 || stderr != "" {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	lines := strings.Split(regexp.MustCompile("\x1b\\[[0-9;]*m").ReplaceAllString(stdout, ""), "\n")
	if len(lines) < 7 || !strings.Contains(lines[0], "all models") || !strings.Contains(lines[0], "requests") {
		t.Fatalf("stdout %q", stdout)
	}
	if f := strings.Fields(lines[1]); len(f) < 7 || f[0]+f[1] != "24hours" || f[2] != "1" || f[3] != "1" || f[4] != "1.5k" || f[5] != "0" || f[6] != "40" {
		t.Errorf("24 hours: %q", lines[1])
	}
	if !strings.Contains(stdout, "model-in-force") || strings.Contains(stdout, "model-on-disk") {
		t.Errorf("not the proxy's sessions:\n%s", stdout)
	}
	if code, _, stderr := captured(t, func() int { return run([]string{"stats", "--all"}) }); code == 0 || !strings.Contains(stderr, "usage: aish stats") {
		t.Errorf("aish stats --all: exit %d, stderr %q", code, stderr)
	}

	t.Setenv("AISH_SOCK", "")
	if code, _, stderr := captured(t, func() int { return run([]string{"stats"}) }); code == 0 || !strings.Contains(stderr, "max_steps") {
		t.Errorf("outside aish with config.toml broken: exit %d, stderr %q", code, stderr)
	}
	if err := os.WriteFile(os.Getenv("AISH_CONFIG"), []byte("sessions_dir = \""+disk.SessionsDir+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, _ = captured(t, func() int { return run([]string{"stats"}) })
	if code != 0 || !strings.Contains(stdout, "model-on-disk") || strings.Contains(stdout, "model-in-force") {
		t.Errorf("outside aish: exit %d, stdout\n%s", code, stdout)
	}
}
