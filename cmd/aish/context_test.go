package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/inebotov/aish/internal/config"
	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/rpc"
	"github.com/inebotov/aish/internal/session"
)

func TestContextArgs(t *testing.T) {
	for _, c := range []struct {
		args []string
		full bool
		ok   bool
	}{
		{nil, false, true},
		{[]string{"--full"}, true, true},
		{[]string{"--bogus"}, false, false},
		{[]string{"full"}, false, false},
		{[]string{"--full", "--full"}, false, false},
	} {
		full, err := contextArgs(c.args)
		if full != c.full || (err == nil) != c.ok {
			t.Errorf("%q: full %v, err %v", c.args, full, err)
		}
		if err != nil && err.Error() != contextUsage {
			t.Errorf("%q: %v", c.args, err)
		}
	}
	code, stderr := runIn(t, "", "", "context", "--bogus")
	if code != 2 || stderr != contextUsage+"\n" {
		t.Errorf("--bogus: exit %d, stderr %q", code, stderr)
	}
}

func TestWriteContext(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleUser, Text: "is a<b && c>d?"},
		{Role: llm.RoleAssistant, Text: "let me look", Raw: json.RawMessage(`{"thinking":"long"}`), Provider: "anthropic", Model: "m",
			ToolCalls: []llm.ToolCall{
				{ID: "c1", Name: "bash", Args: json.RawMessage("{\n  \"command\": \"test a < b && echo <yes>\"\n}")},
				{ID: "c2", Name: "read_file", Args: json.RawMessage(`{"path":`)},
			}},
		{Role: llm.RoleUser, ToolResults: []llm.ToolResult{
			{CallID: "c1", Name: "bash", Content: "<yes>"},
			{CallID: "c2", Name: "read_file", Content: "bad args", IsError: true},
		}},
	}
	var b bytes.Buffer
	if err := writeContext(&b, msgs); err != nil {
		t.Fatal(err)
	}
	want := `{"role":"user","text":"is a<b && c>d?"}` + "\n" +
		`{"role":"assistant","text":"let me look","tool_calls":[{"id":"c1","name":"bash","args":{"command":"test a < b && echo <yes>"}},` +
		`{"id":"c2","name":"read_file","args":"{\"path\":"}],"provider":"anthropic","model":"m","raw_bytes":19}` + "\n" +
		`{"role":"user","tool_results":[{"call_id":"c1","name":"bash","content":"<yes>"},` +
		`{"call_id":"c2","name":"read_file","content":"bad args","is_error":true}]}` + "\n"
	if b.String() != want {
		t.Errorf("JSONL\n%s\nwant\n%s", b.String(), want)
	}
	if strings.Contains(b.String(), "thinking") {
		t.Error("Raw printed")
	}
	b.Reset()
	if err := writeContext(&b, nil); err != nil || b.Len() != 0 {
		t.Errorf("empty context: %q, %v", b.String(), err)
	}
}

func TestToolStats(t *testing.T) {
	call := func(id, name, args string) llm.ToolCall {
		return llm.ToolCall{ID: id, Name: name, Args: json.RawMessage(args)}
	}
	msgs := []llm.Message{
		// The result of a call a clear cut off.
		{Role: llm.RoleUser, ToolResults: []llm.ToolResult{{CallID: "c0", Name: "grep", Content: "12345"}}},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call("c1", "bash", `{"command":"ls"}`), call("c2", "read_file", `{"path":"a"}`)}},
		{Role: llm.RoleUser, ToolResults: []llm.ToolResult{
			{CallID: "c1", Name: "bash", Content: "a\nb\n"},
			{CallID: "c2", Name: "read_file", Content: "no such file", IsError: true},
		}},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call("c3", "bash", `{"command":"cat a"}`), call("c4", "ask_user", `{}`)}},
		{Role: llm.RoleUser, ToolResults: []llm.ToolResult{
			{CallID: "c3", Name: "bash", Content: strings.Repeat("x", 100)},
			{CallID: "c4", Name: "ask_user", Content: "yes"},
		}},
		{Role: llm.RoleAssistant, Text: "done"},
	}
	want := []toolStat{
		{name: "bash", calls: 2, args: 16 + 19, results: 104},
		{name: "read_file", calls: 1, args: 12, results: 12, errors: 1},
		{name: "grep", results: 5},
		{name: "ask_user", calls: 1, args: 2, results: 3},
	}
	if got := toolStats(msgs); !reflect.DeepEqual(got, want) {
		t.Errorf("stats\n%+v\nwant\n%+v", got, want)
	}
	// Ties go by name.
	tie := []llm.Message{
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call("1", "b", `{}`), call("2", "a", `{}`)}},
	}
	if got := toolStats(tie); len(got) != 2 || got[0].name != "a" || got[1].name != "b" {
		t.Errorf("ties %+v", got)
	}
	if got := toolStats([]llm.Message{{Role: llm.RoleUser, Text: "hi"}}); len(got) != 0 {
		t.Errorf("no calls: %+v", got)
	}
}

func TestKindStats(t *testing.T) {
	es := []session.Entry{
		{Kind: session.KindSummary, Text: strings.Repeat("s", 60)},
		{Kind: session.KindAssistant, Text: "ok"},
		{Kind: session.KindShell, Cmd: "cat big", Output: strings.Repeat("x", 5000)},
		{Kind: session.KindUser, Text: "why"},
		{Kind: session.KindShell, Cmd: "ls"},
		{Kind: session.KindToolResult, Output: "yes"},
	}
	want := []kindStat{
		{kind: "shell", entries: 2, bytes: (7 + 1000 + 40) + (2 + 40)},
		{kind: "user", entries: 1, bytes: 3 + 40},
		{kind: "assistant", entries: 1, bytes: 2 + 40},
		{kind: "tool_result", entries: 1, bytes: 3 + 40},
		{kind: "summary", entries: 1, bytes: 60 + 40},
		{kind: "total", entries: 6, bytes: 1089 + 43 + 42 + 43 + 100},
	}
	got := kindStats(es, 1000)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("stats\n%+v\nwant\n%+v", got, want)
	}
	// The same measure as the estimate of the context.
	if total := got[len(got)-1].bytes; total/4 != session.Tokens(es, 1000) {
		t.Errorf("total %d bytes, Tokens %d", total, session.Tokens(es, 1000))
	}
	if got := kindStats(nil, 1000); got != nil {
		t.Errorf("empty context: %+v", got)
	}
	var b bytes.Buffer
	printKinds(&b, got)
	printKinds(&b, want[4:])
	if s := b.String(); !strings.Contains(s, "summary") || !strings.HasSuffix(s, "\x1b[0m        6       1317      329\n") {
		t.Errorf("printed %q", s)
	}
}

// aish context asks the proxy for the status and the journal: the
// statistics go to stderr, the messages, masked as the model gets them,
// to stdout.
func TestContextCmd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Chdir(home)
	secret := "ghp_" + strings.Repeat("a", 36)
	es := []session.Entry{
		{Kind: session.KindShell, Cmd: "cat old", Output: "before the summary"},
		{Kind: session.KindSummary, Text: "we did things"},
		{Kind: session.KindShell, Cmd: "cat token", Output: secret + "\n", Cwd: home},
		{Kind: session.KindUser, Text: "what is it", Cwd: home},
		{Kind: session.KindAssistant, Text: "a token", ToolCalls: []session.ToolCall{{ID: "c1", Name: "bash", Args: json.RawMessage(`{"command":"env"}`)}}},
		{Kind: session.KindToolResult, ToolCallID: "c1", ToolName: "bash", Output: "TOKEN=" + secret},
	}
	l, err := net.Listen("unix", filepath.Join(t.TempDir(), "sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go rpc.Serve(l, func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		switch method {
		case rpc.MethodStatus:
			return rpc.Status{Info: rpc.Info{SessionID: "s1", Window: 200_000}, Tokens: 1234, Commands: 1, Requests: 1}, nil
		case rpc.MethodHistory:
			return es, nil
		}
		return nil, errors.New("unexpected " + method)
	})
	t.Setenv("AISH_SOCK", l.Addr().String())

	cfg := config.Default()
	cfg.MaskDefaults = true
	code, stdout, stderr := captured(t, func() int { return contextCmd(cfg, []string{"--full"}) })
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	for _, s := range []string{"context", "1.2k / 200k (0%), estimated", "by kind", "total", "tools", "bash"} {
		if !strings.Contains(stderr, s) {
			t.Errorf("no %q in stderr:\n%s", s, stderr)
		}
	}
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("stdout\n%s", stdout)
	}
	for _, l := range lines {
		var cl contextLine
		if err := json.Unmarshal([]byte(l), &cl); err != nil {
			t.Errorf("line %q: %v", l, err)
		}
	}
	if !strings.HasPrefix(lines[0], `{"role":"user","text":"<summary>`) || strings.Contains(stdout, "before the summary") {
		t.Errorf("not from the summary on:\n%s", stdout)
	}
	if strings.Contains(stdout, secret) || strings.Contains(stderr, secret) || !strings.Contains(stdout, "ghp_***") {
		t.Errorf("the secret is not masked:\n%s", stdout)
	}

	// Without --full, nothing on stdout.
	if code, stdout, _ := captured(t, func() int { return contextCmd(cfg, nil) }); code != 0 || stdout != "" {
		t.Errorf("without --full: exit %d, stdout %q", code, stdout)
	}
}

// captured runs f with stdout and stderr going to files and returns what
// they got.
func captured(t *testing.T, f func() int) (int, string, string) {
	t.Helper()
	dir := t.TempDir()
	out, err := os.Create(filepath.Join(dir, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	errf, err := os.Create(filepath.Join(dir, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	defer errf.Close()
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = out, errf
	code := f()
	os.Stdout, os.Stderr = oldOut, oldErr
	o, _ := os.ReadFile(out.Name())
	e, _ := os.ReadFile(errf.Name())
	return code, string(o), string(e)
}
