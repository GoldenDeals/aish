package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/session"
	"github.com/inebotov/aish/internal/tools"
)

func TestMaskDefaults(t *testing.T) {
	m, err := NewMasker(true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.Len() != len(defaultMask) {
		t.Fatalf("len %d", m.Len())
	}
	key := "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA\nabcdef\n-----END RSA PRIVATE KEY-----"
	cases := []struct{ in, want string }{
		{"AKIAIOSFODNN7EXAMPLE", "AKIA***"},
		{"id AKIAIOSFODNN7EXAMPLE end", "id AKIA*** end"},
		{"ghp_" + strings.Repeat("a", 36), "ghp_***"},
		{"github_pat_" + strings.Repeat("Z", 82), "gith***"},
		{"sk-proj-abcdefghijklmnopqrstuvwxyz0123", "sk-p***"},
		{"xoxb-123456789012-abcdef", "xoxb***"},
		{key, "-----BEGIN RSA PRIVATE KEY-----\nMIIE***\n-----END RSA PRIVATE KEY-----"},
		{"password: hunter2hunter2", "password: hunt***"},
		{`API_KEY = "abcdefghij"`, `API_KEY = "abcd***"`},
		{"AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG", "AWS_SECRET_ACCESS_KEY=wJal***"},
		{"token=abc123def456,next", "token=abc1***,next"},
		{"two: AKIAIOSFODNN7EXAMPLE AKIAIOSFODNN7EXAMPL2", "two: AKIA*** AKIA***"},
		// Not secrets.
		{"AKIAIOSFODNN7EXAMPL", "AKIAIOSFODNN7EXAMPL"},
		{"akiaiosfodnn7example", "akiaiosfodnn7example"},
		{"ghp_short", "ghp_short"},
		{"github_pat_short", "github_pat_short"},
		{"sk-short", "sk-short"},
		{"xoxb-short", "xoxb-short"},
		{"-----BEGIN CERTIFICATE-----\nMIIE\n-----END CERTIFICATE-----", "-----BEGIN CERTIFICATE-----\nMIIE\n-----END CERTIFICATE-----"},
		{"password: short", "password: short"},
		{"max_tokens = 320000000", "max_tokens = 320000000"},
		{"tokens: 123456789", "tokens: 123456789"},
		{"the password is in the vault", "the password is in the vault"},
	}
	for _, c := range cases {
		if got := m.Mask(c.in); got != c.want {
			t.Errorf("Mask(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMaskConfig(t *testing.T) {
	m, err := NewMasker(false, []string{`corp-[0-9a-f]{32}`})
	if err != nil {
		t.Fatal(err)
	}
	if m.Len() != 1 {
		t.Fatalf("len %d", m.Len())
	}
	in := "AKIAIOSFODNN7EXAMPLE corp-" + strings.Repeat("0", 32)
	if got := m.Mask(in); got != "AKIAIOSFODNN7EXAMPLE corp***" {
		t.Errorf("got %q", got)
	}
	if _, err := NewMasker(true, []string{`(`}); err == nil || !strings.Contains(err.Error(), `mask "("`) {
		t.Errorf("bad pattern: %v", err)
	}
	var nilMask *Masker
	if nilMask.Mask(in) != in || nilMask.Len() != 0 {
		t.Error("nil masker must pass text through")
	}
}

func TestMessagesMask(t *testing.T) {
	m, _ := NewMasker(true, nil)
	const key = "AKIAIOSFODNN7EXAMPLE"
	es := []session.Entry{
		{Kind: session.KindInstructions, Path: "/p/CLAUDE.md", Text: "use " + key, About: "project"},
		{Kind: session.KindFile, Path: "/p/.env", Text: "AWS=" + key},
		{Kind: session.KindShell, Cmd: "echo " + key, Output: key + "\n", Cwd: "/p"},
		{Kind: session.KindUser, Text: "what is " + key + "?", Cwd: "/p"},
		{Kind: session.KindAssistant, Text: "look", ToolCalls: []session.ToolCall{{ID: "t1", Name: "read_file"}}},
		{Kind: session.KindToolResult, ToolCallID: "t1", ToolName: "read_file", Output: "k=" + key},
	}
	ms := Messages(es, 1000, m)
	if len(ms) != 3 {
		t.Fatalf("messages %d", len(ms))
	}
	txt := ms[0].Text
	if strings.Count(txt, key) != 2 || strings.Count(txt, "AKIA***") != 3 {
		t.Errorf("first message:\n%s", txt)
	}
	if !strings.Contains(txt, "$ echo "+key+"\nAKIA***\n") || !strings.HasSuffix(txt, "what is "+key+"?") {
		t.Errorf("user text and command kept, output masked:\n%s", txt)
	}
	if r := ms[2].ToolResults[0].Content; r != "k=AKIA***" {
		t.Errorf("tool result %q", r)
	}
	if plain := Messages(es, 1000, nil); strings.Count(plain[0].Text, key) != 5 {
		t.Errorf("nil masker:\n%s", plain[0].Text)
	}
}

// A key printed by a user command reaches the model masked, while the
// journal keeps it; the mask follows the config of each request.
func TestStartMasks(t *testing.T) {
	const key = "AKIAIOSFODNN7EXAMPLE"
	prov := &fakeProvider{replies: []*llm.Response{{Text: "AKIA***"}, {Text: "again"}}}
	a, j, _, _, cwd := newAgent(t, prov)
	j.es = append(j.es, session.Entry{Kind: session.KindShell, Cmd: "echo " + key, Output: key + "\n", Exit: 0, Cwd: cwd})
	if err := a.Start(context.Background(), "what did I print?", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	sent := prov.requests[0].Messages[0].Text
	if strings.Contains(sent, "\n"+key+"\n") || !strings.Contains(sent, "\nAKIA***\n") {
		t.Errorf("sent:\n%s", sent)
	}
	if j.es[0].Output != key+"\n" {
		t.Errorf("journal changed: %q", j.es[0].Output)
	}
	a.Cfg.MaskDefaults = false
	if err := a.Start(context.Background(), "and now?", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if sent := prov.requests[1].Messages[0].Text; !strings.Contains(sent, "\n"+key+"\n") {
		t.Errorf("mask_defaults = false, sent:\n%s", sent)
	}
}
