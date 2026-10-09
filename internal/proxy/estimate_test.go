package proxy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/agent"
	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/session"
	"github.com/GoldenDeals/aish/internal/shellstate"
)

// After a summary, till the next turn is measured, the estimate carries
// the system prompt and the tool schemas the agent will send with it; a
// measured turn has them counted already.
func TestContextSizeOverhead(t *testing.T) {
	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(sess)
	p.overhead = 3000
	if est, _ := p.contextSize(nil); est.Tokens != 0 || est.Measured {
		t.Errorf("an empty journal: %+v", est)
	}
	es := []session.Entry{
		{Kind: session.KindUser, Text: "q"},
		{Kind: session.KindAssistant, Text: "a", InputTokens: 50_000},
		{Kind: session.KindSummary, Text: "what was done"},
		{Kind: session.KindShell, Cmd: "ls", Output: "a b c\n"},
		{Kind: session.KindShell, Cmd: "pwd", Output: "/home\n"},
	}
	est, _ := p.contextSize(es)
	if bare := session.Tokens(es, p.maxOutput, 0).Tokens; est.Tokens <= bare || est.Measured {
		t.Errorf("after a summary: %+v; %d without the overhead", est, bare)
	}
	es = append(es, session.Entry{Kind: session.KindAssistant, Text: "b", InputTokens: 3000})
	if est, _ := p.contextSize(es); est.Tokens != 3000 || !est.Measured {
		t.Errorf("after a measured turn: %+v", est)
	}
	if est, _ := p.contextSize([]session.Entry{{Kind: session.KindClear}}); est.Tokens != 0 {
		t.Errorf("a cleared journal: %+v", est)
	}
}

// The status at the prompt and `aish status` count the overhead: compact?
// goes on when the agent would compact, not a turn later.
func TestStatusCountsOverhead(t *testing.T) {
	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(sess)
	p.model, p.window, p.compactAt = "m", 10_000, 0.5
	if err := sess.Append(session.Entry{Kind: session.KindSummary, Text: strings.Repeat("s", 4000)}); err != nil {
		t.Fatal(err)
	}
	if text, _ := p.statusText(); strings.Contains(text, "compact?") {
		t.Errorf("no overhead known: %q", text)
	}
	p.overhead = 15000
	if text, _ := p.statusText(); !strings.Contains(text, "compact?") {
		t.Errorf("with the overhead: %q", text)
	}
	want := session.Tokens(sess.Entries(), p.maxOutput, 15000)
	if st := p.status(); st.Tokens != want.Tokens || st.Measured || st.Overhead != 15000 {
		t.Errorf("aish status: %d tokens, measured %v, overhead %d; want %d", st.Tokens, st.Measured, st.Overhead, want.Tokens)
	}
}

// A request leaves the proxy what its system prompt and tools weigh.
func TestRequestKeepsOverhead(t *testing.T) {
	prov := &scripted{replies: []*llm.Response{{Text: "hi"}}}
	p, _, cwd := hosted(t, prov)
	p.marker(Marker{Kind: "ask-start"})
	if _, err := call(t, p, rpc.MethodAgentStart, rpc.AgentParams{Text: "hello", Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	got := p.overhead
	p.mu.Unlock()
	if want := p.ag.Overhead(); got == 0 || got != want {
		t.Errorf("overhead %d, the agent's %d", got, want)
	}
}

// The status at the prompt, `aish context` and the agent's compact_at
// size the context alike: by the config a request from the shell's
// directory goes by, the project's max_output_bytes included, and by the
// same window.
func TestContextSizeAgrees(t *testing.T) {
	prov := &scripted{replies: []*llm.Response{
		{Text: "hi", InputTokens: 20_000, OutputTokens: 300, DroppedTokens: 200},
		{Text: "there"},
	}}
	p, _, cwd := hosted(t, prov)
	if err := os.WriteFile(filepath.Join(cwd, config.ProjectFile), []byte("max_output_bytes = 100\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.applyFields(config.Default()) // as Run takes config.toml, none here
	p.window = 200_000
	p.mu.Unlock()
	p.marker(Marker{Kind: "ask-start"})
	if _, err := call(t, p, rpc.MethodAgentStart, rpc.AgentParams{Text: "hello", Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	// The user's commands since, back at the prompt in cwd.
	big := strings.Repeat("x", 5000)
	if err := p.sess.Append(
		session.Entry{Kind: session.KindShell, Cmd: "cat a", Output: big, Cwd: cwd},
		session.Entry{Kind: session.KindShell, Cmd: "cat b", Output: big, Cwd: cwd},
	); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.cur = &shellstate.State{Cwd: cwd}
	es := p.sess.Entries()
	status, cfg := p.contextSize(es)
	st := p.status()
	p.mu.Unlock()

	a := p.ag
	if a.Cfg.MaxOutputBytes != 100 {
		t.Fatalf("the agent's max_output_bytes %d, not the project's", a.Cfg.MaxOutputBytes)
	}
	// What the agent counts (agent.contextSize).
	want := session.Tokens(es, a.Cfg.MaxOutputBytes, a.Overhead())
	if !want.Measured || want.Tokens <= 20_100 {
		t.Fatalf("the agent's estimate %+v", want)
	}
	if status != want {
		t.Errorf("the status %+v, the agent %+v", status, want)
	}
	// What aish context counts: the config in force for its directory,
	// the overhead from aish status.
	res, err := p.configFor(context.Background(), rpc.ConfigParams{Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	if got := session.Tokens(es, res.Config.MaxOutputBytes, st.Overhead); got != want {
		t.Errorf("aish context %+v, the agent %+v", got, want)
	}
	if st.Tokens != want.Tokens || !st.Measured {
		t.Errorf("aish status %d tokens, measured %v; want %d", st.Tokens, st.Measured, want.Tokens)
	}
	// By config.toml's max_output_bytes the commands would weigh more.
	if wrong := session.Tokens(es, config.Default().MaxOutputBytes, a.Overhead()); wrong.Tokens <= want.Tokens {
		t.Errorf("config.toml's max_output_bytes gives %d, the project's %d", wrong.Tokens, want.Tokens)
	}
	// The same limit: compact_at of the same window.
	window := res.Config.ContextWindow
	if window <= 0 {
		window = st.Window
	}
	ctxCfg := res.Config
	ctxCfg.ContextWindow = window
	if l := agent.CompactLimit(a.Cfg); l == 0 || agent.CompactLimit(cfg) != l || agent.CompactLimit(ctxCfg) != l {
		t.Errorf("limits: agent %d, status %d, aish context %d", l, agent.CompactLimit(cfg), agent.CompactLimit(ctxCfg))
	}
}

// The status sizes by the project file of a directory no request came
// from yet without reading it: the first request there takes the file
// as it is then, an edit made after the prompt included.
func TestStatusLeavesProjectUnread(t *testing.T) {
	prov := &scripted{replies: []*llm.Response{{Text: "hi"}, {Text: "there"}}}
	p, _, cwd := hosted(t, prov)
	p.marker(Marker{Kind: "ask-start"})
	if _, err := call(t, p, rpc.MethodAgentStart, rpc.AgentParams{Text: "hello", Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.applyFields(config.Default())
	p.mu.Unlock()
	other := filepath.Join(cwd, "other")
	file := filepath.Join(other, config.ProjectFile)
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("max_output_bytes = 50\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	got := p.sizeConfig(other).MaxOutputBytes
	p.mu.Unlock()
	if want := config.Default().MaxOutputBytes; got != want {
		t.Errorf("max_output_bytes %d of a file no request read; want config.toml's %d", got, want)
	}
	if err := os.WriteFile(file, []byte("max_output_bytes = 70\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p.marker(Marker{Kind: "cmd-end", Payload: "0;" + other})
	p.marker(Marker{Kind: "ask-start"})
	if _, err := call(t, p, rpc.MethodAgentStart, rpc.AgentParams{Text: "and here?", Cwd: other}); err != nil {
		t.Fatal(err)
	}
	if got := p.ag.Cfg.MaxOutputBytes; got != 70 {
		t.Errorf("the request took max_output_bytes %d; want the file as edited, 70", got)
	}
	// Read by the request, it sizes the status from then on: in its
	// directory and below.
	sub := filepath.Join(other, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	inDir, below := p.sizeConfig(other).MaxOutputBytes, p.sizeConfig(sub).MaxOutputBytes
	p.mu.Unlock()
	if inDir != 70 || below != 70 {
		t.Errorf("max_output_bytes %d in the directory, %d below it; want 70", inDir, below)
	}
}
