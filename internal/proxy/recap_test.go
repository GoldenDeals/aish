package proxy

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/session"
)

// `aish recap` retells the session on the screen and leaves the journal
// as it was.
func TestRecapRPC(t *testing.T) {
	prov := &scripted{replies: []*llm.Response{{Text: "hi there"}, {Text: "You said hello."}}}
	p, out, cwd := hosted(t, prov)
	p.marker(Marker{Kind: "ask-start"})
	if _, err := call(t, p, rpc.MethodAgentStart, rpc.AgentParams{Text: "hello", Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	p.marker(Marker{Kind: "cmd-end", Payload: "0;" + cwd})
	before := journalKinds(p.sess)
	if _, err := call(t, p, rpc.MethodRecap, rpc.AgentParams{Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	if got := journalKinds(p.sess); got != before {
		t.Errorf("journal %q, was %q", got, before)
	}
	if !strings.Contains(out.String(), "You said hello.") {
		t.Errorf("terminal %q", out.String())
	}
}

// Only the user recaps, from the shell's foreground and between
// requests, as with compact.
func TestRecapRefused(t *testing.T) {
	prov := &scripted{replies: []*llm.Response{{Text: "never"}}}
	p, _, cwd := hosted(t, prov)
	ap := rpc.AgentParams{Cwd: cwd}
	if err := p.sess.Append(session.Entry{Kind: session.KindUser, Text: "hello", Cwd: cwd}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.mu.Lock()
	p.reqCtx = ctx
	p.mu.Unlock()
	if _, err := call(t, p, rpc.MethodRecap, ap); !errors.Is(err, errBusy) {
		t.Errorf("recap during a request: %v", err)
	}
	p.mu.Lock()
	p.reqCtx = nil
	p.handed = "c1"
	p.mu.Unlock()
	if _, err := call(t, p, rpc.MethodRecap, ap); !errors.Is(err, errNested) {
		t.Errorf("recap from the agent's command: %v", err)
	}
	p.mu.Lock()
	p.handed = ""
	p.fg = func() (int, error) { return 1, nil } // init's group, not this one
	p.mu.Unlock()
	if _, err := call(t, p, rpc.MethodRecap, ap); !errors.Is(err, errNotShell) {
		t.Errorf("recap from the background: %v", err)
	}
	if prov.calls != 0 {
		t.Errorf("%d calls to the model", prov.calls)
	}
	if got := journalKinds(p.sess); got != "user" {
		t.Errorf("journal %q", got)
	}
}
