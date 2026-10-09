package agent

import (
	"context"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/session"
	"github.com/GoldenDeals/aish/internal/tools"
)

// lockedJournal is a journal in memory that subagents in goroutines of
// their own may append to, as the proxy's session lets them; id may change
// as clear changes it.
type lockedJournal struct {
	mu sync.Mutex
	id string
	es []session.Entry
}

func (j *lockedJournal) ID() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.id
}

func (j *lockedJournal) Len() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.es)
}

func (j *lockedJournal) Entries() []session.Entry {
	j.mu.Lock()
	defer j.mu.Unlock()
	return slices.Clone(j.es)
}

func (j *lockedJournal) Append(es ...session.Entry) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.es = append(j.es, es...)
	return nil
}

func (j *lockedJournal) setID(id string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.id = id
}

// usageOf is the KindUsage entries of es.
func usageOf(es []session.Entry) []session.Entry {
	return slices.DeleteFunc(slices.Clone(es), func(e session.Entry) bool { return e.Kind != session.KindUsage })
}

// costly is r with what the API would say it cost.
func costly(r *llm.Response, in, cached, out int) *llm.Response {
	r.InputTokens, r.CachedTokens, r.OutputTokens = in, cached, out
	return r
}

// spendProvider is a provider whose host follows host and whose subagents
// answer "NAME reply", at the cost of 300 tokens in, 100 of them cached,
// and 20 out, once their gate, if any, is open.
func spendProvider(host func(llm.Request) *llm.Response, gates map[string]chan struct{}) *subProvider {
	prov := &subProvider{}
	prov.answer = func(ctx context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
		name := subOf(req)
		if name == "" {
			return reply(host(req), onText)
		}
		if g := gates[name]; g != nil {
			select {
			case <-g:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return reply(costly(&llm.Response{Text: name + " reply"}, 300, 100, 20), onText)
	}
	return prov
}

// checkUnseen fails when es, with the usage entries in it, looks otherwise
// than without them to what measures the context and what the model gets.
func checkUnseen(t *testing.T, es []session.Entry) {
	t.Helper()
	without := withoutUsage(es)
	if len(without) == len(es) {
		t.Fatal("no usage entries to check")
	}
	if got, want := session.Tokens(es, 1000, 500), session.Tokens(without, 1000, 500); got != want {
		t.Errorf("session.Tokens %+v with usage, %+v without", got, want)
	}
	last, prev := measured(session.Current(es))
	wantLast, wantPrev := measured(session.Current(without))
	if !reflect.DeepEqual(last, wantLast) || !reflect.DeepEqual(prev, wantPrev) {
		t.Errorf("measured %+v, %+v; want %+v, %+v", last, prev, wantLast, wantPrev)
	}
	if last == nil || last.Kind != session.KindAssistant {
		t.Errorf("the last measured turn %+v", last)
	}
	mask, _ := NewMasker(true, nil)
	if got, want := Messages(es, 1000, mask), Messages(without, 1000, mask); !reflect.DeepEqual(got, want) {
		t.Errorf("Messages with usage\n%+v\nwithout\n%+v", got, want)
	}
}

// A subagent of task leaves what each of its turns cost in the host's
// journal, between the call and its result; what measures the context and
// what the model gets do not see it.
func TestSubagentSpendInJournal(t *testing.T) {
	prov := spendProvider(func(req llm.Request) *llm.Response {
		return costly(host(`[{"agent":"alpha","prompt":"job A"}]`)(req), 1000, 800, 50)
	}, nil)
	a, _, _, _, cwd := newSubAgent(t, prov, def("alpha"))
	j := &lockedJournal{id: "s1"}
	a.Journal = j
	if err := a.Start(context.Background(), "do it", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	es := j.Entries()
	if got := kinds(es); got != "user assistant usage tool_result assistant" {
		t.Fatalf("journal %s", got)
	}
	u := es[2]
	want := session.Usage{Input: 300, Cached: 100, Output: 20}
	if u.About != "alpha" || u.Provider != "fake" || u.Model != "m" || u.Usage == nil || *u.Usage != want || u.Time.IsZero() {
		t.Errorf("usage entry %+v, usage %+v", u, u.Usage)
	}
	if u.InputTokens != 0 || u.CachedTokens != 0 || u.OutputTokens != 0 {
		t.Errorf("usage in the tokens of a turn: %+v", u)
	}
	checkUnseen(t, es)
	// The host's second turn got the conversation without it.
	reqs := prov.all()
	if got, want := reqs[len(reqs)-1].Messages, Messages(withoutUsage(es[:4]), a.Cfg.MaxOutputBytes, a.mask); !reflect.DeepEqual(got, want) {
		t.Errorf("the host's turn after the call got\n%+v\nwant\n%+v", got, want)
	}
}

// A subagent in the background leaves what its turns cost from its own
// goroutine, after the request that started it; once the shell has left
// that session, nothing.
func TestBackgroundSpendInJournal(t *testing.T) {
	for _, left := range []bool{false, true} {
		gate := make(chan struct{})
		prov := spendProvider(hostTurns(
			costly(callOf("t1", subName, `{"tasks":[{"agent":"beta","prompt":"job B"}],"background":true}`), 1000, 0, 40),
			costly(&llm.Response{Text: "started it"}, 1100, 1000, 30),
		), map[string]chan struct{}{"beta": gate})
		a, _, _, _, cwd := newBgAgent(t, prov, def("beta"))
		j := &lockedJournal{id: "s1"}
		a.Journal = j
		if err := a.Start(context.Background(), "start beta", tools.Exec{Dir: cwd}); err != nil {
			t.Fatal(err)
		}
		if left {
			j.setID("s2") // aish clear, before it stops the subagent
		}
		close(gate)
		eventually(t, a, "bg1 beta: ok")
		es := j.Entries()
		if left {
			if u := usageOf(es); len(u) != 0 {
				t.Errorf("the next session got %+v", u)
			}
			continue
		}
		if got := kinds(es); got != "user assistant tool_result assistant usage" {
			t.Fatalf("journal %s", got)
		}
		if u := es[4]; u.About != "beta" || u.Usage == nil || *u.Usage != (session.Usage{Input: 300, Cached: 100, Output: 20}) {
			t.Errorf("usage entry %+v", u)
		}
		checkUnseen(t, es)
	}
}

// The agent holds no usage entries, and does not read the journal again
// for them: what it holds otherwise than the journal stays. Another entry
// it did not write is news.
func TestLoadPassesOverUsage(t *testing.T) {
	a, _, _, _, _ := newAgent(t, &fakeProvider{})
	j := &lockedJournal{id: "s1"}
	a.Journal = j
	now := time.Now()
	j.Append(session.Entry{Kind: session.KindUsage, Time: now, Usage: &session.Usage{Input: 1}},
		session.Entry{Kind: session.KindUser, Time: now, Text: "hi"})
	a.load(true)
	if got := kinds(a.entries); got != "user" {
		t.Fatalf("entries %s", got)
	}
	a.entries[0].Text = "cut" // as cutResults does
	j.Append(session.Entry{Kind: session.KindUsage, Time: now, Usage: &session.Usage{Input: 1}})
	a.load(false)
	if a.entries[0].Text != "cut" || a.seen != 3 {
		t.Errorf("read again for usage: %+v, seen %d", a.entries, a.seen)
	}
	if err := a.append(session.Entry{Kind: session.KindAssistant, Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	a.load(false)
	if a.entries[0].Text != "cut" {
		t.Errorf("read again after its own entry: %+v", a.entries)
	}
	j.Append(session.Entry{Kind: session.KindShell, Time: now, Cmd: "ls"})
	a.load(false)
	if got := kinds(a.entries); a.entries[0].Text != "hi" || got != "user assistant shell" {
		t.Errorf("not read again for a command: %s %+v", got, a.entries)
	}
}

// A subagent in the background may write between the entries the host
// makes of one request: Messages is the same as without its entry.
func TestMessagesSkipUsage(t *testing.T) {
	now := time.Now()
	es := []session.Entry{
		{Kind: session.KindInstructions, Time: now, Path: "/a/CLAUDE.md", Text: "one"},
		{Kind: session.KindUsage, Time: now, About: "beta", Usage: &session.Usage{Input: 300, Output: 20}},
		{Kind: session.KindInstructions, Time: now, Path: "/a/b/CLAUDE.md", Text: "two"},
		{Kind: session.KindUser, Time: now, Text: "go", Cwd: "/a/b"},
	}
	mask, _ := NewMasker(true, nil)
	got, want := Messages(es, 1000, mask), Messages(withoutUsage(es), 1000, mask)
	if !reflect.DeepEqual(got, want) || len(got) != 1 {
		t.Errorf("Messages with usage\n%+v\nwithout\n%+v", got, want)
	}
}
