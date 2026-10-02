package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inebotov/aish/internal/config"
	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/rpc"
	"github.com/inebotov/aish/internal/session"
)

// scripted answers each Complete with the next reply; block holds a
// Complete until the context ends.
type scripted struct {
	mu      sync.Mutex
	replies []*llm.Response
	calls   int
	block   bool
}

func (s *scripted) Name() string                                    { return "fake" }
func (s *scripted) Model() string                                   { return "m" }
func (s *scripted) Efforts() []string                               { return []string{"low", "high", "xhigh", "max"} }
func (s *scripted) MaxTokens(string) int64                          { return 0 }
func (s *scripted) Models(context.Context) ([]llm.ModelInfo, error) { return nil, nil }
func (s *scripted) Complete(ctx context.Context, _ llm.Request, onText func(string)) (*llm.Response, error) {
	s.mu.Lock()
	n := s.calls
	s.calls++
	block := s.block
	s.mu.Unlock()
	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if n >= len(s.replies) {
		return nil, errors.New("no reply scripted")
	}
	r := s.replies[n]
	if onText != nil && r.Text != "" {
		onText(r.Text)
	}
	return r, nil
}

// terminal is the proxy's output, safe to read while the proxy writes.
type terminal struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (t *terminal) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.b.Write(p)
}

func (t *terminal) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.b.String()
}

// hosted is a proxy with the agent over a scripted provider, in a home of
// its own so that no file of the machine is read.
func hosted(t *testing.T, prov *scripted) (*Proxy, *terminal, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AISH_CONFIG", filepath.Join(home, "none.toml"))
	cwd := filepath.Join(home, "work")
	if err := os.Mkdir(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	sess, err := session.New(filepath.Join(home, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	p := New(sess)
	out := &terminal{}
	p.out = out
	p.run = t.TempDir()
	p.model = "m"
	p.newProvider = func(config.Config) (llm.Provider, error) { return prov, nil }
	return p, out, cwd
}

func call(t *testing.T, p *Proxy, method string, params any) (any, error) {
	t.Helper()
	b, _ := json.Marshal(params)
	return p.handle(context.Background(), method, b)
}

func journalKinds(sess *session.Session) string {
	var ks []string
	for _, e := range sess.Entries() {
		ks = append(ks, e.Kind)
	}
	return strings.Join(ks, " ")
}

// A request over RPC: the bash command is left for the shell, its output
// comes back through the markers, the next call goes on from there.
func TestAgentRequest(t *testing.T) {
	prov := &scripted{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "bash", Args: json.RawMessage(`{"command":"ls"}`)}}},
		{Text: "two files"},
	}}
	p, out, cwd := hosted(t, prov)
	p.marker(Marker{Kind: "ask-start"})
	if _, err := call(t, p, rpc.MethodAgentStart, rpc.AgentParams{Text: "list files", Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	cmd, _ := os.ReadFile(filepath.Join(p.run, "next.cmd"))
	id, _ := os.ReadFile(filepath.Join(p.run, "next.id"))
	if string(cmd) != "ls" || string(id) != "c1\n" {
		t.Fatalf("handed off %q as %q", cmd, id)
	}
	if got := journalKinds(p.sess); got != "user assistant" {
		t.Fatalf("journal %s", got)
	}
	if s := out.String(); !strings.Contains(s, "❯\x1b[0m \x1b[1mls") || strings.Contains(s, "\x1b]6973;") {
		t.Errorf("terminal %q", s)
	}

	// The shell runs the command.
	p.marker(Marker{Kind: "agent-start", Payload: "c1;ls"})
	p.output([]byte("a\r\nb\r\n"))
	p.marker(Marker{Kind: "agent-end", Payload: "c1;0;" + cwd})
	if _, err := call(t, p, rpc.MethodAgentResume, rpc.AgentParams{ID: "c1", RC: 0, Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	if got := journalKinds(p.sess); got != "user assistant tool_result assistant" {
		t.Fatalf("journal %s", got)
	}
	es := p.sess.Entries()
	if !strings.HasPrefix(es[2].Output, "a\nb\n") || !strings.HasSuffix(es[2].Output, "[exit 0, cwd "+cwd+"]") {
		t.Errorf("bash result %q", es[2].Output)
	}
	if s := out.String(); !strings.Contains(s, "two files\r\n") {
		t.Errorf("answer not on the terminal with CRLF: %q", s)
	}
	if p.ag == nil || p.ag.Tools == nil || p.agentProv != prov {
		t.Error("the agent and its provider are not kept")
	}

	v, err := call(t, p, rpc.MethodStatus, nil)
	if err != nil {
		t.Fatal(err)
	}
	if st := v.(rpc.Status); st.Requests != 1 || st.ToolCalls != 1 || st.Tokens == 0 || st.SessionID != p.sess.ID {
		t.Errorf("status %+v", st)
	}
}

// agent_cancel stops the request in progress, as the client asks when
// Ctrl+C reaches it.
func TestAgentCancel(t *testing.T) {
	prov := &scripted{block: true}
	p, _, cwd := hosted(t, prov)
	done := make(chan error, 1)
	go func() {
		_, err := call(t, p, rpc.MethodAgentStart, rpc.AgentParams{Text: "wait", Cwd: cwd})
		done <- err
	}()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		p.mu.Lock()
		running := p.cancelReq != nil
		p.mu.Unlock()
		if running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the request never started")
		}
	}
	if _, err := call(t, p, rpc.MethodAgentCancel, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("request ended with %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the request goes on after agent_cancel")
	}
	if p.cancelReq != nil {
		t.Error("cancelReq left behind")
	}
	// Nothing to cancel is fine.
	if _, err := call(t, p, rpc.MethodAgentCancel, nil); err != nil {
		t.Error(err)
	}
}

// The agent's question is answered on the keyboard the proxy reads: the
// answer is echoed, not sent to the shell; Ctrl+C is.
func TestAskKey(t *testing.T) {
	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(sess)
	out := &terminal{}
	p.out = out
	type answer struct {
		s   string
		err error
	}
	res := make(chan answer, 1)
	go func() {
		s, err := p.askUser(context.Background(), "allow? ")
		res <- answer{s, err}
	}()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		p.mu.Lock()
		open := p.ask != nil
		p.mu.Unlock()
		if open {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the question never opened")
		}
	}
	if got := p.key([]byte("yx")); got != nil {
		t.Errorf("typed answer went to the shell: %q", got)
	}
	if got := p.key([]byte{0x7f}); got != nil {
		t.Errorf("backspace went to the shell: %q", got)
	}
	if got := p.key([]byte("\x1b[A")); got != nil {
		t.Errorf("an arrow went to the shell: %q", got)
	}
	if got := p.key([]byte{0x03}); !bytes.Equal(got, []byte{0x03}) {
		t.Errorf("Ctrl+C did not reach the shell: %q", got)
	}
	if got := p.key([]byte("\rls\n")); string(got) != "ls\n" {
		t.Errorf("what followed the answer was lost: %q", got)
	}
	select {
	case a := <-res:
		if a.err != nil || a.s != "y" {
			t.Errorf("answer %q, %v", a.s, a.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no answer")
	}
	if s := out.String(); s != "allow? yx\b \b\r\n" {
		t.Errorf("echo %q", s)
	}
	if p.ask != nil {
		t.Error("the question stayed open")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.askUser(ctx, "again? "); !errors.Is(err, context.Canceled) {
		t.Errorf("interrupted question: %v", err)
	}
	if p.ask != nil {
		t.Error("an interrupted question stayed open")
	}
}

func TestCRLF(t *testing.T) {
	var c crlf
	got := string(c.fix([]byte("a\nb\r\nc\r"))) + string(c.fix([]byte("\nd"))) + string(c.fix([]byte("\n")))
	if got != "a\r\nb\r\nc\r\nd\r\n" {
		t.Errorf("%q", got)
	}
}

// An external tool's output is shown folded and kept for Ctrl+O, as a
// command's is; a long built-in result is kept too.
func TestLiveFold(t *testing.T) {
	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(sess)
	out := &terminal{}
	p.out = out
	u := &ui{p: p}
	l := u.Live("⚙ probe")
	l.Write([]byte("one\ntwo\n"))
	if p.tool == nil {
		t.Fatal("no live fold")
	}
	l.Finish(0)
	if p.tool != nil || len(p.folds) != 1 || p.folds[0].Title != "⚙ probe" || p.folds[0].Text != "one\r\ntwo\r\n" {
		t.Errorf("folds %+v, tool %v", p.folds, p.tool)
	}
	if s := out.String(); !strings.Contains(s, "2 lines") || strings.Contains(s, "one") {
		t.Errorf("terminal %q", s)
	}
	u.Fold("⚙ read_file x", "1\n2\n")
	if len(p.folds) != 2 {
		t.Errorf("folds %+v", p.folds)
	}

	p.foldLines = -1
	l = u.Live("⚙ raw")
	l.Write([]byte("raw\n"))
	l.Finish(0)
	if p.tool != nil || len(p.folds) != 2 || !strings.HasSuffix(out.String(), "raw\r\n") {
		t.Errorf("without folding: folds %d, terminal %q", len(p.folds), out.String())
	}
}
