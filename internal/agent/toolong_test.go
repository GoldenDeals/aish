package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/session"
	"github.com/inebotov/aish/internal/tools"
)

// errTooLong is the API's refusal of a request past the window, as the SDK
// gives it.
func errTooLong() error {
	e := &anthropic.Error{}
	_ = e.UnmarshalJSON([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 210000 tokens > 200000 maximum"}}`))
	e.StatusCode = http.StatusBadRequest
	e.Request = httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	e.Response = &http.Response{StatusCode: http.StatusBadRequest}
	return e
}

// rejectingProvider fails the requests fails numbers and answers the
// others as the fakeProvider it wraps: replies are indexed by request,
// failed ones included.
type rejectingProvider struct {
	*fakeProvider
	fails map[int]error
}

func (p *rejectingProvider) Complete(ctx context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
	if err, ok := p.fails[len(p.requests)]; ok {
		p.requests = append(p.requests, req)
		return nil, err
	}
	return p.fakeProvider.Complete(ctx, req, onText)
}

// newRejected is an agent whose journal already holds a request answered,
// so that there is something to sum up, with compact_at on and the window
// unknown: only the API says the context does not fit.
func newRejected(t *testing.T, fails map[int]error, replies ...*llm.Response) (*Agent, *fakeJournal, *rejectingProvider, *fakeUI, string) {
	t.Helper()
	fake := &fakeProvider{replies: replies}
	a, j, _, ui, cwd := newAgent(t, fake)
	prov := &rejectingProvider{fakeProvider: fake, fails: fails}
	a.Provider = prov
	a.Cfg.ContextWindow, a.Cfg.CompactAt = 0, 0.8
	j.es = []session.Entry{
		{Kind: session.KindUser, Text: "what is here", Cwd: cwd},
		{Kind: session.KindAssistant, Text: "a lot", InputTokens: 1000},
	}
	return a, j, prov, ui, cwd
}

func summaryRequest(req llm.Request) bool {
	m := req.Messages
	return len(m) > 0 && strings.Contains(m[len(m)-1].Text, "compacted automatically")
}

// A turn the API rejects as too long for the window has the session summed
// up and is made again, though the estimate said the context fits.
func TestPromptTooLongCompacts(t *testing.T) {
	a, j, prov, ui, cwd := newRejected(t, map[int]error{0: errTooLong()},
		nil, &llm.Response{Text: "the user looked around"}, &llm.Response{Text: "it is 42"})
	if err := a.Start(context.Background(), "go on", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if got := kinds(j.es); got != "user assistant user summary user assistant" || j.es[len(j.es)-1].Text != "it is 42" {
		t.Fatalf("journal %s: %+v", got, j.es)
	}
	if len(prov.requests) != 3 || !summaryRequest(prov.requests[1]) {
		t.Fatalf("%d requests, the second %+v", len(prov.requests), prov.requests[1].Messages)
	}
	if next := prov.requests[2].Messages; len(next) != 1 || !strings.Contains(next[0].Text, "<summary>") || !strings.Contains(next[0].Text, "go on") {
		t.Errorf("after the summary the model got %+v", next)
	}
	out := ui.String()
	if !strings.Contains(out, "[aish: the context does not fit the window; compacting]") || !strings.Contains(out, "compacted: ") {
		t.Errorf("terminal:\n%s", out)
	}
	if a.windowFull {
		t.Error("the window is still counted full")
	}
}

// A turn rejected again right after the summary ends the request: the
// session is not summed up over and over.
func TestPromptTooLongAfterSummary(t *testing.T) {
	a, j, prov, ui, cwd := newRejected(t, map[int]error{0: errTooLong(), 2: errTooLong()},
		nil, &llm.Response{Text: "the user looked around"})
	err := a.Start(context.Background(), "go on", tools.Exec{Dir: cwd})
	if !llm.PromptTooLong(err) {
		t.Fatalf("error %v", err)
	}
	if len(prov.requests) != 3 {
		t.Errorf("%d requests", len(prov.requests))
	}
	if got := kinds(j.es); got != "user assistant user summary user" {
		t.Errorf("journal %s", got)
	}
	if out := ui.String(); strings.Count(out, "compacting") != 2 { // the reason and autoCompact's line
		t.Errorf("terminal:\n%s", out)
	}
}

// A summary that cannot be made does not get asked for again within the
// request: the turn after it is rejected as well, and that is the end.
func TestPromptTooLongSummaryFails(t *testing.T) {
	a, j, prov, ui, cwd := newRejected(t, map[int]error{0: errTooLong(), 1: errTooLong(), 2: errTooLong()})
	err := a.Start(context.Background(), "go on", tools.Exec{Dir: cwd})
	if !llm.PromptTooLong(err) {
		t.Fatalf("error %v", err)
	}
	// The history has no outputs to cut: the summary is asked for once.
	if len(prov.requests) != 3 || !summaryRequest(prov.requests[1]) || summaryRequest(prov.requests[2]) {
		t.Errorf("%d requests", len(prov.requests))
	}
	if got := kinds(j.es); got != "user assistant user" {
		t.Errorf("journal %s", got)
	}
	if out := ui.String(); !strings.Contains(out, "could not compact") {
		t.Errorf("terminal:\n%s", out)
	}
}

// With compact_at = 0 nothing is summed up: the user is told what frees
// the window.
func TestPromptTooLongCompactOff(t *testing.T) {
	a, _, prov, ui, cwd := newRejected(t, map[int]error{0: errTooLong()})
	a.Cfg.CompactAt = 0
	err := a.Start(context.Background(), "go on", tools.Exec{Dir: cwd})
	if want := "invalid_request_error: prompt is too long: 210000 tokens > 200000 maximum; aish compact frees it"; err == nil || err.Error() != want {
		t.Fatalf("error %v", err)
	}
	if len(prov.requests) != 1 || strings.Contains(ui.String(), "compacting") {
		t.Errorf("%d requests, terminal:\n%s", len(prov.requests), ui.String())
	}
}

// A summary rejected as too long is asked for again with the outputs in
// the history cut; the journal keeps them whole.
func TestPromptTooLongSummaryCut(t *testing.T) {
	big := strings.Repeat("a line of the log\n", 6000)
	a, j, prov, ui, cwd := newRejected(t, map[int]error{0: errTooLong(), 1: errTooLong()},
		nil, nil, &llm.Response{Text: "the user read the log"}, &llm.Response{Text: "done"})
	j.es = append(j.es,
		session.Entry{Kind: session.KindShell, Cmd: "cat log", Output: big, Cwd: cwd},
		session.Entry{Kind: session.KindUser, Text: "read it", Cwd: cwd},
		session.Entry{Kind: session.KindAssistant, ToolCalls: []session.ToolCall{{ID: "c1", Name: "read_file", Args: []byte(`{"path":"log"}`)}}},
		session.Entry{Kind: session.KindToolResult, ToolCallID: "c1", ToolName: "read_file", Output: big},
		session.Entry{Kind: session.KindAssistant, Text: "it is long"},
	)
	if err := a.Start(context.Background(), "go on", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if len(prov.requests) != 4 || !summaryRequest(prov.requests[1]) || !summaryRequest(prov.requests[2]) {
		t.Fatalf("%d requests", len(prov.requests))
	}
	size := func(req llm.Request) int {
		n := 0
		for _, m := range req.Messages {
			n += len(m.Text)
			for _, r := range m.ToolResults {
				n += len(r.Content)
			}
		}
		return n
	}
	if first, cut := size(prov.requests[1]), size(prov.requests[2]); first < len(big) || cut > 20000 {
		t.Errorf("the summary was asked for with %d bytes, then %d", first, cut)
	}
	if got := kinds(j.es); got != "user assistant shell user assistant tool_result assistant user summary user assistant" {
		t.Fatalf("journal %s", got)
	}
	for _, e := range j.es {
		if (e.Kind == session.KindShell || e.Kind == session.KindToolResult) && e.Output != big {
			t.Errorf("the journal's %s was cut to %d bytes", e.Kind, len(e.Output))
		}
	}
	if !strings.Contains(ui.String(), "compacted: ") {
		t.Errorf("terminal:\n%s", ui.String())
	}
}

// A summary the window or max_tokens cut is not taken for a whole one; one
// the window cut is asked for again with the outputs cut, as one rejected.
func TestSummaryCut(t *testing.T) {
	big := strings.Repeat("x", 100000)
	for _, tc := range []struct {
		name    string
		replies []*llm.Response
		err     string
	}{
		{"max_tokens", []*llm.Response{{Text: "the user", StopReason: llm.StopMaxTokens}}, "the summary was cut at max_tokens"},
		{"window", []*llm.Response{
			{Text: "the user", StopReason: llm.StopContextWindow},
			{Text: "the user read", StopReason: llm.StopContextWindow},
		}, "the summary was cut: the context window is full"},
		{"window, then whole", []*llm.Response{
			{Text: "the user", StopReason: llm.StopContextWindow},
			{Text: "the user read x"},
		}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, j, prov, _, cwd := newRejected(t, nil, tc.replies...)
			j.es = append(j.es, session.Entry{Kind: session.KindShell, Cmd: "cat x", Output: big, Cwd: cwd})
			err := a.Compact(context.Background(), "", tools.Exec{Dir: cwd})
			if tc.err == "" {
				if err != nil || kinds(j.es) != "user assistant shell summary" || j.es[3].Text != "the user read x" {
					t.Errorf("error %v, journal %s", err, kinds(j.es))
				}
			} else if err == nil || err.Error() != tc.err || kinds(j.es) != "user assistant shell" {
				t.Errorf("error %v, journal %s", err, kinds(j.es))
			}
			if len(prov.requests) != len(tc.replies) {
				t.Errorf("%d requests", len(prov.requests))
			}
		})
	}
}

// A refusal is said to be one: the model may give no text at all.
func TestRefusal(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{{StopReason: llm.StopRefusal}}}
	a, j, _, ui, cwd := newAgent(t, prov)
	if err := a.Start(context.Background(), "do it", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if got := kinds(j.es); got != "user assistant" {
		t.Errorf("journal %s", got)
	}
	if out := ui.String(); !strings.Contains(out, "[aish: the model declined to answer]") {
		t.Errorf("terminal:\n%s", out)
	}
}

// A turn the API paused goes on in the same request, from the paused reply
// as it came; at most three times in a row.
func TestPauseTurn(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{
		{Text: "searching", Raw: []byte(`{"role":"assistant"}`), StopReason: llm.StopPause},
		{Text: "found it"},
	}}
	a, j, _, ui, cwd := newAgent(t, prov)
	if err := a.Start(context.Background(), "find it", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if got := kinds(j.es); got != "user assistant assistant" || j.es[2].Text != "found it" || len(prov.requests) != 2 {
		t.Fatalf("journal %s after %d requests", got, len(prov.requests))
	}
	if m := prov.requests[1].Messages; m[len(m)-1].Role != llm.RoleAssistant || m[len(m)-1].Text != "searching" || string(m[len(m)-1].Raw) == "" {
		t.Errorf("the turn went on from %+v", m[len(m)-1])
	}
	if strings.Contains(ui.String(), "paused") {
		t.Errorf("terminal:\n%s", ui.String())
	}

	pause := &llm.Response{Text: "still searching", StopReason: llm.StopPause}
	prov = &fakeProvider{replies: []*llm.Response{pause, pause, pause, pause, {Text: "never"}}}
	a, j, _, ui, cwd = newAgent(t, prov)
	if err := a.Start(context.Background(), "find it", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if got := kinds(j.es); got != "user assistant assistant assistant assistant" || len(prov.requests) != 4 {
		t.Errorf("journal %s after %d requests", got, len(prov.requests))
	}
	if !strings.Contains(ui.String(), "[aish: the model paused its turn; ask to continue]") {
		t.Errorf("terminal:\n%s", ui.String())
	}
}
