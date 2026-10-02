package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/session"
	"github.com/inebotov/aish/internal/tools"
)

// inOrder tells whether each of subs is in s, after the one before it.
func inOrder(s string, subs ...string) bool {
	for _, sub := range subs {
		i := strings.Index(s, sub)
		if i < 0 {
			return false
		}
		s = s[i+len(sub):]
	}
	return true
}

// What a user-prompt hook adds is an entry of its own: the request is
// recorded as typed, and the model gets the hook's text masked, in the
// message of the request, before it.
func TestHookContextEntry(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{{Text: "on main"}}}
	a, j, _, ui, cwd := newAgent(t, prov)
	a.Cfg.HooksDir = ""
	hook(t, a, "user-prompt", "branch", `echo '{"context": "AKIAIOSFODNN7EXAMPLE branch main"}'`)
	if err := a.Start(context.Background(), "which branch", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if got := kinds(j.es); got != "context user assistant" {
		t.Fatalf("journal %s", got)
	}
	if c := j.es[0]; c.Text != "AKIAIOSFODNN7EXAMPLE branch main" || c.About != "branch" || c.Cwd != cwd {
		t.Errorf("context %+v", c)
	}
	if u := j.es[1]; u.Text != "which branch" {
		t.Errorf("the request was recorded as %q", u.Text)
	}
	msgs := prov.requests[0].Messages
	if len(msgs) != 1 {
		t.Fatalf("the model got %d messages: %+v", len(msgs), msgs)
	}
	txt := msgs[0].Text
	if !inOrder(txt, "<system-reminder>\nAdded by the user-prompt hook branch:\nAKIA*** branch main\n</system-reminder>", "which branch") {
		t.Errorf("the model got %q", txt)
	}
	if strings.Contains(txt, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("the key went to the model unmasked: %q", txt)
	}
	if !strings.Contains(ui.String(), "(user-prompt/branch: AKIAIOSFODNN7EXAMPLE branch main)") {
		t.Errorf("terminal:\n%s", ui.String())
	}
}

// Two hooks give two entries, in the order they ran, and the model gets
// both in that order.
func TestHookContextTwoHooks(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{{Text: "ok"}}}
	a, j, _, _, cwd := newAgent(t, prov)
	a.Cfg.HooksDir = ""
	hook(t, a, "user-prompt", "10-branch", `echo '{"context": "branch main"}'`)
	hook(t, a, "user-prompt", "20-time", `echo '{"context": "at noon"}'`)
	if err := a.Start(context.Background(), "go", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if got := kinds(j.es); got != "context context user assistant" {
		t.Fatalf("journal %s", got)
	}
	if j.es[0].About != "10-branch" || j.es[1].About != "20-time" {
		t.Errorf("hooks %q, %q", j.es[0].About, j.es[1].About)
	}
	txt := prov.requests[0].Messages[0].Text
	if !inOrder(txt, "hook 10-branch:\nbranch main", "hook 20-time:\nat noon", "\ngo") {
		t.Errorf("the model got %q", txt)
	}
}

// Compacted at the start of a request, the context is carried over with
// the request: after the summary, before what the user typed.
func TestHookContextAfterAutoCompact(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{{Text: "the user built the project"}, {Text: "it built"}}}
	a, j, _, _, cwd := newAgent(t, prov)
	a.Cfg.ContextWindow = 32000
	a.Cfg.HooksDir = ""
	hook(t, a, "user-prompt", "branch", `echo '{"context": "branch main"}'`)
	j.es = []session.Entry{
		{Kind: session.KindUser, Text: "hi", Cwd: cwd},
		{Kind: session.KindAssistant, Text: "hello", InputTokens: 24000},
		{Kind: session.KindShell, Cmd: "make", Output: strings.Repeat("x", 30000), Cwd: cwd},
	}
	if err := a.Start(context.Background(), "did it build", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if got := kinds(j.es); got != "user assistant shell context user summary context user assistant" {
		t.Fatalf("journal %s", got)
	}
	if typed, copied := j.es[3], j.es[6]; copied.Text != typed.Text || copied.About != typed.About {
		t.Errorf("the context after the summary %+v, before %+v", copied, typed)
	}
	next := prov.requests[1].Messages
	if len(next) != 1 || !inOrder(next[0].Text, "the user built the project", "hook branch:\nbranch main", "did it build") {
		t.Errorf("after the summary the model got %+v", next)
	}
}
