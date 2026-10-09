package proxy

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/session"
)

// bashReply is a reply of the model that runs cmd as call id.
func bashReply(id, cmd string) *llm.Response {
	args, _ := json.Marshal(map[string]string{"command": cmd})
	return &llm.Response{ToolCalls: []llm.ToolCall{{ID: id, Name: "bash", Args: args}}}
}

// request has the shell ask text of p's agent, run the command it hands
// off, call id, which prints out, and come back to the prompt.
func request(t *testing.T, p *Proxy, cwd, text, id, cmd, out string) {
	t.Helper()
	p.marker(Marker{Kind: "ask-start"})
	if _, err := call(t, p, rpc.MethodAgentStart, rpc.AgentParams{Text: text, Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	p.marker(Marker{Kind: "agent-start", Payload: id + ";" + cmd})
	p.output([]byte(out))
	p.marker(Marker{Kind: "agent-end", Payload: id + ";0;" + cwd})
	if _, err := call(t, p, rpc.MethodAgentResume, rpc.AgentParams{ID: id, Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	p.marker(Marker{Kind: "cmd-end", Payload: "0;" + cwd})
}

// historyProxy is a proxy whose agent ran two requests, "list files" and
// "count them", with a bash command each, both called c1: some providers
// count the ids of a request from one.
func historyProxy(t *testing.T, between func(p *Proxy)) (*Proxy, string) {
	t.Helper()
	prov := &scripted{replies: []*llm.Response{
		bashReply("c1", "ls"), {Text: "two files"},
		bashReply("c1", "wc -l"), {Text: "2"},
	}}
	p, _, cwd := hosted(t, prov)
	p.size = func() (int, int) { return 80, 24 }
	request(t, p, cwd, "list files", "c1", "ls", "a\r\nb\r\n")
	if between != nil {
		between(p)
	}
	request(t, p, cwd, "count them", "c1", "wc -l", "2\r\n")
	return p, cwd
}

// viewerAtPrompt opens the viewer with Ctrl+O at the prompt and returns its
// rows, a line between the outputs marked with "=", closing it again.
func viewerAtPrompt(t *testing.T, p *Proxy) []string {
	t.Helper()
	v := openViewer(t, p)
	defer p.key([]byte("q"))
	p.mu.Lock()
	defer p.mu.Unlock()
	rows := slices.Clone(v.rows)
	for i := range rows {
		if v.sep[i] {
			rows[i] = "=" + rows[i]
		}
	}
	return rows
}

// The viewer shows the calls of every request of the session, each after
// its text: the last one's folds whole, as they came, those before it from
// the journal, as the model got them.
func TestViewerTwoRequests(t *testing.T) {
	p, cwd := historyProxy(t, nil)
	want := []string{
		"=? list files", "❯ ls", "a", "b", "[exit 0, cwd " + cwd + "]",
		"",
		"=? count them", "❯ wc -l", "2",
	}
	if rows := viewerAtPrompt(t, p); !slices.Equal(rows, want) {
		t.Errorf("rows\n%q, want\n%q", rows, want)
	}
	// aish expand keeps to the folds of the last request.
	v, err := call(t, p, rpc.MethodFolds, nil)
	if err != nil {
		t.Fatal(err)
	}
	if folds := v.([]Fold); len(folds) != 1 || folds[0].Title != "❯ wc -l" {
		t.Errorf("aish expand: %+v", folds)
	}
}

// Ctrl+L between two requests is a line in the viewer, not where it
// begins.
func TestViewerClearBetween(t *testing.T) {
	var cleared []string
	p, cwd := historyProxy(t, func(p *Proxy) {
		p.output([]byte("\x1b[H\x1b[2J"))
		cleared = viewerAtPrompt(t, p)
	})
	first := []string{"=? list files", "❯ ls", "a", "b", "[exit 0, cwd " + cwd + "]", "", "=── screen cleared ──"}
	if !slices.Equal(cleared, first) {
		t.Errorf("after Ctrl+L\n%q, want\n%q", cleared, first)
	}
	want := append(first, "=? count them", "❯ wc -l", "2")
	if rows := viewerAtPrompt(t, p); !slices.Equal(rows, want) {
		t.Errorf("rows\n%q, want\n%q", rows, want)
	}
}

// The viewer open while a request begins keeps showing the one before it,
// now from the journal: it is not left empty for a frame.
func TestViewerNextRequest(t *testing.T) {
	prov := &scripted{replies: []*llm.Response{bashReply("c1", "ls"), {Text: "two files"}}}
	p, out, cwd := hosted(t, prov)
	p.size = func() (int, int) { return 80, 24 }
	request(t, p, cwd, "list files", "c1", "ls", "a\r\nb\r\n")
	v := openViewer(t, p)
	defer p.key([]byte("q"))
	p.marker(Marker{Kind: "ask-start"})
	p.viewFrame(v)
	rows := lastFrame(out.String())
	if !slices.Contains(rows, "❯ ls") || !slices.Contains(rows, "[exit 0, cwd "+cwd+"]") {
		t.Errorf("frame %q", rows)
	}
}

// writeSession is a session saved in dir whose agent ran ls for "list
// files", closed again for another aish to open.
func writeSession(t *testing.T, dir string) string {
	t.Helper()
	s, err := session.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Append(
		session.Entry{Kind: session.KindShell, Cmd: "make", Output: "ok"},
		session.Entry{Kind: session.KindUser, Text: "list files"},
		session.Entry{Kind: session.KindAssistant, ToolCalls: []session.ToolCall{{ID: "c1", Name: "bash", Args: json.RawMessage(`{"command":"ls"}`)}}},
		session.Entry{Kind: session.KindToolResult, ToolCallID: "c1", ToolName: "bash", Output: "a\n[exit 0, cwd /tmp]"},
		session.Entry{Kind: session.KindAssistant, Text: "one file"},
	)
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	s.Unlock()
	return s.ID
}

var resumedRows = []string{"=? list files", "❯ ls", "a", "[exit 0, cwd /tmp]"}

// A resumed session has no folds, only its journal: Ctrl+O at the prompt
// shows its calls.
func TestViewerResumed(t *testing.T) {
	dir := t.TempDir()
	sess, err := session.Load(dir, writeSession(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	p := New(sess)
	p.out = &terminal{}
	p.size = func() (int, int) { return 80, 24 }
	if rows := viewerAtPrompt(t, p); !slices.Equal(rows, resumedRows) {
		t.Errorf("rows\n%q, want\n%q", rows, resumedRows)
	}
	p.key([]byte("q"))

	// The journal is read once while it stays as it is.
	p.mu.Lock()
	a, b := p.viewFolds(), p.viewFolds()
	p.mu.Unlock()
	if &a[0] != &b[0] {
		t.Error("the journal read again")
	}
}

// aish resume inside: the viewer shows the session switched to, not the
// folds of the one left.
func TestViewerResumeInside(t *testing.T) {
	prov := &scripted{replies: []*llm.Response{bashReply("c1", "pwd"), {Text: "here"}}}
	p, _, cwd := hosted(t, prov)
	p.size = func() (int, int) { return 80, 24 }
	if err := p.sess.Save(); err != nil { // on disk, the other is called otherwise
		t.Fatal(err)
	}
	request(t, p, cwd, "where am I", "c1", "pwd", cwd+"\r\n")
	id := writeSession(t, filepath.Join(filepath.Dir(cwd), "sessions"))
	if _, err := call(t, p, rpc.MethodResume, rpc.ResumeParams{ID: id}); err != nil {
		t.Fatal(err)
	}
	if rows := viewerAtPrompt(t, p); !slices.Equal(rows, resumedRows) {
		t.Errorf("rows\n%q, want\n%q", rows, resumedRows)
	}
}

// aish clear begins a session of no calls: the folds of the last request
// were the other's, and Ctrl+O at the prompt is readline's again.
func TestViewerAfterClear(t *testing.T) {
	prov := &scripted{replies: []*llm.Response{bashReply("c1", "pwd"), {Text: "here"}}}
	p, _, cwd := hosted(t, prov)
	p.size = func() (int, int) { return 80, 24 }
	request(t, p, cwd, "where am I", "c1", "pwd", cwd+"\r\n")
	if _, err := call(t, p, rpc.MethodClear, rpc.ClearParams{}); err != nil {
		t.Fatal(err)
	}
	if b := p.key([]byte{ctrlO}); string(b) != "\x0f" || p.view != nil {
		t.Errorf("Ctrl+O gave %q to the shell, viewer %v", b, p.view != nil)
	}
}

// Without a call of the agent in the session Ctrl+O at the prompt is
// readline's, as before: the requests and commands alone show nothing.
func TestViewerEmptySession(t *testing.T) {
	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(sess)
	p.out = &terminal{}
	p.size = func() (int, int) { return 80, 24 }
	check := func(when string) {
		t.Helper()
		if b := p.key([]byte{ctrlO}); string(b) != "\x0f" || p.view != nil {
			t.Errorf("%s: Ctrl+O gave %q to the shell, viewer %v", when, b, p.view != nil)
		}
	}
	check("empty")
	_ = sess.Append(
		session.Entry{Kind: session.KindShell, Cmd: "ls", Output: "a"},
		session.Entry{Kind: session.KindUser, Text: "hi"},
		session.Entry{Kind: session.KindAssistant, Text: "hello"},
		session.Entry{Kind: session.KindClear},
	)
	check("no calls")
}

// pastCalls pairs each call with the result that follows its reply, the
// ids of one request apart from another's, a result of a call cut short
// coming with the next request; it marks a summary, and titles a built-in
// as the screen does, another tool by its arguments in their order.
func TestPastCalls(t *testing.T) {
	tc := func(id, name, args string) session.ToolCall {
		return session.ToolCall{ID: id, Name: name, Args: json.RawMessage(args)}
	}
	es := []session.Entry{
		{Kind: session.KindUser, Text: "hi"},
		{Kind: session.KindAssistant, Text: "hello"},
		{Kind: session.KindUser, Text: "read\nit"},
		{Kind: session.KindAssistant, ToolCalls: []session.ToolCall{
			tc("1", "read_file", `{"limit":5,"path":"x.go"}`),
			tc("2", "mcp__gh__search", `{"q":"aish","n":3,"body":"`+strings.Repeat("x", 90)+`"}`),
		}},
		{Kind: session.KindToolResult, ToolCallID: "1", Output: "package x"},
		{Kind: session.KindToolResult, ToolCallID: "2", Output: "none", IsError: true},
		{Kind: session.KindAssistant, ToolCalls: []session.ToolCall{tc("1", "bash", `{"command":"sleep 9"}`)}},
		{Kind: session.KindSummary, Text: "sum"},
		{Kind: session.KindToolResult, ToolCallID: "1", Output: "interrupted by the user"},
		{Kind: session.KindUser, Text: "again"},
		{Kind: session.KindAssistant, ToolCalls: []session.ToolCall{tc("1", "bash", `{"command":"true"}`)}},
		{Kind: session.KindToolResult, ToolCallID: "1", Output: "[exit 0, cwd /]"},
		{Kind: session.KindUser, Text: "live"},
		{Kind: session.KindAssistant, ToolCalls: []session.ToolCall{tc("1", "bash", `{"command":"date"}`)}},
	}
	want := []Fold{
		{Text: "? read\n  it"},
		{Title: "⚙ read_file x.go 5", Text: "package x"},
		{Title: "⚙ mcp__gh__search aish 3 <90 bytes>", Text: "none"},
		{Title: "❯ sleep 9", Text: "interrupted by the user"},
		{Text: "── compacted ──"},
		{Text: "? again"},
		{Title: "❯ true", Text: "[exit 0, cwd /]"},
	}
	calls, ask, asked := pastCalls(es, len(es)-2)
	if !slices.Equal(calls, want) {
		t.Errorf("calls\n%q, want\n%q", calls, want)
	}
	if ask != "live" || !asked {
		t.Errorf("the request of the folds: %q %v", ask, asked)
	}
	if _, _, asked := pastCalls(es, len(es)); asked {
		t.Error("the last request's line is due with its calls shown")
	}
}
