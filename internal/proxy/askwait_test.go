package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/session"
)

// termProxy is a proxy with a terminal 80 columns wide and no shell.
func termProxy(t *testing.T) (*Proxy, *terminal) {
	t.Helper()
	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(sess)
	out := &terminal{}
	p.out = out
	p.size = func() (int, int) { return 80, 24 }
	return p, out
}

// A question nobody answers ends at the deadline of its ctx, the agent's
// time for it: the line is left with No and why, as an answer would leave
// it, and the keyboard goes to the shell again.
func TestAskTimeout(t *testing.T) {
	const yes = "\x1b[7m[ Yes ]\x1b[27m   No  "
	for _, tc := range []struct {
		cause error
		end   string
	}{
		{errors.New("no answer in 30ms"), "No (no answer in 30ms)"},
		{nil, "No"},
	} {
		p, out := termProxy(t)
		ctx, cancel := context.WithTimeoutCause(context.Background(), 30*time.Millisecond, tc.cause)
		start := time.Now()
		ans, err := p.askUser(ctx, "allow?")
		took := time.Since(start)
		cancel()
		if ans != "" || !errors.Is(err, context.DeadlineExceeded) || took < 30*time.Millisecond {
			t.Errorf("%s: %q, %v after %v", tc.end, ans, err, took)
		}
		if s, want := modeless(out.String()), "\x1b[?25lallow? "+yes+"\x1b[14D"+tc.end+"\x1b[K\r\n\x1b[?25h"; s != want {
			t.Errorf("terminal\n got %q\nwant %q", s, want)
		}
		if p.ask != nil {
			t.Error("the question stayed open")
		}
		if got := p.key([]byte("y")); string(got) != "y" {
			t.Errorf("%s: the keys after it: %q", tc.end, got)
		}
		if pasteModeOf(out.String()) == "on" {
			t.Errorf("%s: bracketed paste left on: %q", tc.end, out.String())
		}
	}
}

// The form of ask_user goes at the deadline of its ctx as on Ctrl+C:
// erased, no answers, the keyboard the shell's again.
func TestFormTimeout(t *testing.T) {
	p, out := termProxy(t)
	ctx, cancel := context.WithTimeoutCause(context.Background(), 30*time.Millisecond, errors.New("no answer in 30ms"))
	defer cancel()
	ans, err := p.askForm(ctx, twoQuestions())
	if ans != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("%+v, %v", ans, err)
	}
	if s := modeless(out.String()); !strings.HasSuffix(s, "\x1b[J\x1b[A\x1b[?25h") || p.form != nil {
		t.Errorf("left %q, form %v", s, p.form)
	}
	if got := p.key([]byte("ls")); string(got) != "ls" {
		t.Errorf("the keys after it: %q", got)
	}
}

// An answer given as the time runs out stands: the screen shows it.
func TestAnsweredAtDeadline(t *testing.T) {
	p, _ := termProxy(t)
	ctx, cancel := context.WithCancel(context.Background())
	res := make(chan string, 1)
	go func() {
		ans, err := p.askUser(ctx, "allow?")
		if err != nil {
			ans = err.Error()
		}
		res <- ans
	}()
	waitOpen(t, p, func() bool { return p.ask != nil })
	pause(p) // the keys of a question come after one (askguard.go)
	p.mu.Lock()
	cancel()
	time.Sleep(20 * time.Millisecond) // askUser is past its select, waiting for p.mu
	p.keysRead([]byte("n"))
	p.askKey([]byte("n"))
	p.mu.Unlock()
	if ans := <-res; ans != "n" {
		t.Errorf("the question: %q", ans)
	}

	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	form := make(chan formResult, 1)
	go func() {
		ans, err := p.askForm(ctx, twoQuestions())
		form <- formResult{ans, err}
	}()
	waitOpen(t, p, func() bool { return p.form != nil })
	pause(p)
	p.mu.Lock()
	cancel()
	time.Sleep(20 * time.Millisecond)
	p.keysRead([]byte(keyDownSeq + keyEnterSeq))
	p.formKey([]byte(keyDownSeq + keyEnterSeq))
	p.formKey([]byte("2"))
	p.formKey([]byte(keyEnterSeq))
	p.mu.Unlock()
	if r := result(t, form); r.err != nil || len(r.ans) != 2 || r.ans[0].String() != "Patch" || r.ans[1].String() != "Icons" {
		t.Errorf("the form: %+v", r)
	}
}

// waitOpen waits for open, a question on the terminal, true under p.mu.
func waitOpen(t *testing.T, p *Proxy, open func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		p.mu.Lock()
		ok := open()
		p.mu.Unlock()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the question never opened")
		}
	}
}

// A request whose form of ask_user nobody answers goes on after
// ask_timeout of config.toml: the model hears that nobody answered.
func TestAskUserTimeoutRequest(t *testing.T) {
	args := `{"questions":[{"question":"Which approach?","header":"Approach","options":[{"label":"Rewrite"},{"label":"Patch"}]}]}`
	prov := &scripted{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "ask_user", Args: json.RawMessage(args)}}},
		{Text: "going on without it"},
	}}
	p, out, cwd := hosted(t, prov)
	if err := os.WriteFile(os.Getenv("AISH_CONFIG"), []byte("ask_timeout = \"100ms\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p.size = func() (int, int) { return 80, 24 }
	p.marker(Marker{Kind: "ask-start"})
	start := time.Now()
	if _, err := call(t, p, rpc.MethodAgentStart, rpc.AgentParams{Text: "make it nice", Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took < 100*time.Millisecond {
		t.Errorf("the form went after %v", took)
	}
	if got := journalKinds(p.sess); got != "user assistant tool_result assistant" {
		t.Fatalf("journal %s", got)
	}
	if r := p.sess.Entries()[2]; r.IsError || !strings.Contains(r.Output, "did not answer in 100ms") {
		t.Errorf("result %+v", r)
	}
	if s := out.String(); !strings.Contains(s, "✗ no answer in 100ms") || !strings.Contains(s, "going on without it") || p.form != nil {
		t.Errorf("terminal %q, form %v", s, p.form)
	}
}
