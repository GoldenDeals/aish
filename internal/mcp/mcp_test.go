package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GoldenDeals/aish/internal/tools"
)

// The test binary doubles as a stub MCP server on stdio.
func TestMain(m *testing.M) {
	if os.Getenv("AISH_MCP_STUB") == "1" {
		stub()
		return
	}
	os.Exit(m.Run())
}

const stubSchema = `{"type":"object","properties":{
	"query":{"type":"string","description":"What to find"},
	"limit":{"type":"integer"},
	"exact":{"type":"boolean"},
	"filter":{"type":"object"},
	"tags":{"type":["array","null"]},
	"author":{"type":"string"}},
	"required":["query","author"]}`

func stub() {
	in := bufio.NewScanner(os.Stdin)
	starts, _ := os.OpenFile(os.Getenv("AISH_MCP_STARTS"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	fmt.Fprintln(starts, "start")
	reply := func(id json.RawMessage, result string) {
		fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":%s}`+"\n", id, result)
	}
	var schema bytes.Buffer
	json.Compact(&schema, []byte(stubSchema))
	for in.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		json.Unmarshal(in.Bytes(), &m)
		switch m.Method {
		case "initialize":
			// A server may ask things of the client; aish must answer and go on.
			fmt.Println(`{"jsonrpc":"2.0","id":"s1","method":"roots/list"}`)
			reply(m.ID, `{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"stub"}}`)
		case "tools/list":
			reply(m.ID, `{"tools":[{"name":"search","description":"Search things","inputSchema":`+schema.String()+`},`+
				`{"name":"echo","inputSchema":{"type":"object"}},{"name":"crash"}]}`)
		case "tools/call":
			switch m.Params.Name {
			case "search":
				if m.Params.Arguments["query"] == "hang" {
					continue
				}
				b, _ := json.Marshal(m.Params.Arguments)
				reply(m.ID, fmt.Sprintf(`{"content":[{"type":"text","text":%q},{"type":"image","mimeType":"image/png","data":%q}]}`,
					b, base64.StdEncoding.EncodeToString([]byte("PNG"))))
			case "crash":
				fmt.Fprintln(os.Stderr, "boom: out of cheese")
				os.Exit(3)
			default:
				reply(m.ID, `{"content":[{"type":"text","text":"no such thing"}],"isError":true}`)
			}
		}
	}
}

func stubManager(t *testing.T, cache, starts string) *Manager {
	t.Helper()
	exe, _ := os.Executable()
	cfg := map[string]Server{"stub": {Command: exe, Env: map[string]string{"AISH_MCP_STUB": "1", "AISH_MCP_STARTS": starts}}}
	m := NewManager(cfg, cache)
	t.Cleanup(m.Close)
	return m
}

// TestManagerWriteNotes: a cache that could not be written is a note in
// `aish mcp`, not lost silently.
func TestManagerWriteNotes(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "cache")
	os.WriteFile(cache, nil, 0o600) // a file where the directory should be
	m := stubManager(t, cache, filepath.Join(dir, "starts"))
	m.List(context.Background(), true)
	notes := m.Status().Notes
	if len(notes) != 1 || !strings.HasPrefix(notes[0], "stub: tool list not cached: ") {
		t.Errorf("notes %q, want just the cache's", notes)
	}
}

func TestManager(t *testing.T) {
	dir := t.TempDir()
	cache, starts := filepath.Join(dir, "cache"), filepath.Join(dir, "starts")

	ctx := context.Background()
	m := stubManager(t, cache, starts)
	if res := m.List(ctx, false); len(res.Tools) != 0 {
		t.Fatalf("listed without starting: %+v", res)
	}
	if st := m.Status().Servers; len(st) != 1 || st[0].State != "new" || st[0].Transport != "stdio" {
		t.Errorf("status before start: %+v", st)
	}
	res := m.List(ctx, true)
	if st := m.Status(); st.Servers[0].State != "running" || len(st.Servers[0].Tools) != 3 || len(st.Notes) != 0 {
		t.Errorf("status: %+v", st)
	}
	var names []string
	for _, ti := range res.Tools {
		names = append(names, ti.Name)
	}
	if want := []string{"stub_search", "stub_echo", "stub_crash"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("tools %q, want %q", names, want)
	}
	if len(res.Errors) != 0 {
		t.Errorf("errors %q", res.Errors)
	}

	raw, err := m.Call(ctx, "stub_search", map[string]any{"query": "x"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := Format(raw)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(out, "\n")
	if lines[0] != `{"query":"x"}` || !strings.HasSuffix(lines[1], ".png") {
		t.Fatalf("output %q", out)
	}
	defer os.Remove(lines[1])
	if b, _ := os.ReadFile(lines[1]); string(b) != "PNG" {
		t.Errorf("image file holds %q", b)
	}

	raw, err = m.Call(ctx, "stub_echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := Format(raw); out != "no such thing" || err == nil {
		t.Errorf("isError: %q, %v", out, err)
	}

	if _, err := m.Call(ctx, "stub_crash", nil); err == nil || !strings.Contains(err.Error(), "out of cheese") {
		t.Errorf("crash: %v", err)
	}
	// The next call starts the server again.
	if _, err := m.Call(ctx, "stub_search", map[string]any{"query": "y"}); err != nil {
		t.Errorf("after a crash: %v", err)
	}
	if _, err := m.Call(ctx, "nope", nil); err == nil {
		t.Error("unknown tool")
	}

	// The next shell knows the tools from the cache and starts nothing.
	m.Close()
	before, _ := os.ReadFile(starts)
	m2 := stubManager(t, cache, starts)
	if res := m2.List(ctx, true); len(res.Tools) != 3 {
		t.Fatalf("from cache: %+v", res)
	}
	if after, _ := os.ReadFile(starts); len(after) != len(before) {
		t.Error("listing from the cache started the server")
	}
	if st := m2.Status().Servers[0]; st.State != "idle" || len(st.Tools) != 3 {
		t.Errorf("status from the cache: %+v", st)
	}
}

// Local gives the agent in the proxy the manager's tools without an RPC
// round trip; the model is not given schemas of commands-only servers.
func TestLocal(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	m := stubManager(t, filepath.Join(dir, "cache"), filepath.Join(dir, "starts"))
	defer m.Close()
	if ts, _ := Local(ctx, m, false); len(ts) != 0 {
		t.Fatalf("listed without starting: %d tools", len(ts))
	}
	ts, problems := Local(ctx, m, true)
	if len(ts) != 3 || len(problems) != 0 {
		t.Fatalf("%d tools, problems %q", len(ts), problems)
	}
	search := ts[0]
	if search.Name() != "stub_search" || tools.ServerOf(search) != "stub" || !tools.IsHidden(search) || len(search.Args()) != 6 {
		t.Errorf("tool %+v", search)
	}
	if tools.Streams(search) {
		t.Error("the output of an MCP tool comes at the end")
	}
	out, err := search.Execute(ctx, tools.Exec{}, map[string]any{"query": "x"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(out, "\n"); lines[0] != `{"query":"x"}` {
		t.Errorf("output %q", out)
	} else {
		os.Remove(lines[1])
	}
	if out, err := ts[1].Execute(ctx, tools.Exec{}, nil, nil); out != "no such thing" || err == nil {
		t.Errorf("isError: %q, %v", out, err)
	}
}

// The model gets a tool's schema as its server gave it, with what the
// CLI form drops.
func TestRemoteSchema(t *testing.T) {
	raw := `{"type":"object","properties":{"q":{"type":"string","enum":["a","b"]}}}`
	ts, problems := convert(ListResult{Tools: []ToolInfo{
		{Name: "s_enum", Schema: json.RawMessage(raw)},
		{Name: "s_none"},
	}}, nil)
	if len(ts) != 2 || len(problems) != 0 {
		t.Fatalf("%d tools, problems %q", len(ts), problems)
	}
	want := map[string]any{"type": "object", "properties": map[string]any{
		"q": map[string]any{"type": "string", "enum": []any{"a", "b"}},
	}}
	if got := ts[0].Schema(); !reflect.DeepEqual(got, want) {
		t.Errorf("raw schema: got %#v", got)
	}
	// The API takes an object schema, "required": [] included.
	if b, _ := json.Marshal(ts[1].Schema()); string(b) != `{"properties":{},"required":[],"type":"object"}` {
		t.Errorf("no schema: %s", b)
	}
}

// A client that gives up stops waiting at once, but neither the start it
// asked for nor the server is lost for the others.
func TestGiveUp(t *testing.T) {
	dir := t.TempDir()
	starts := filepath.Join(dir, "starts")
	m := stubManager(t, filepath.Join(dir, "cache"), starts)

	gone, cancel := context.WithCancel(context.Background())
	cancel()
	if res := m.List(gone, true); len(res.Errors) != 1 || !strings.Contains(res.Errors[0], "context canceled") {
		t.Fatalf("cancelled listing: %+v", res)
	}
	if res := m.List(context.Background(), true); len(res.Tools) != 3 || len(res.Errors) != 0 {
		t.Fatalf("listing after it: %+v", res)
	}
	if b, _ := os.ReadFile(starts); string(b) != "start\n" {
		t.Errorf("starts %q, want one", b)
	}
	if res := m.List(context.Background(), false); res.Tools[0].Timeout != startTimeout+callTimeout {
		t.Errorf("timeout %v", res.Tools[0].Timeout)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := m.Call(ctx, "stub_search", map[string]any{"query": "hang"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("hung call: %v", err)
	}
	if _, err := m.Call(context.Background(), "stub_search", map[string]any{"query": "z"}); err != nil {
		t.Errorf("call after it: %v", err)
	}
	if st := m.Status().Servers[0]; st.State != "running" {
		t.Errorf("status %+v", st)
	}
}

func TestBrokenServer(t *testing.T) {
	m := NewManager(map[string]Server{"bad": {Command: "/nonexistent/server"}}, t.TempDir())
	defer m.Close()
	res := m.List(context.Background(), true)
	if len(res.Tools) != 0 || len(res.Errors) != 1 || !strings.HasPrefix(res.Errors[0], "bad: ") {
		t.Fatalf("%+v", res)
	}
	if st := m.Status().Servers[0]; st.State != "failed" || st.Error == "" || st.Failed.IsZero() {
		t.Errorf("status %+v", st)
	}
}

func TestMask(t *testing.T) {
	got := strings.Join(maskArgs([]string{"srv", "--token", "abc", "--api-key=def", "GITHUB_TOKEN=ghi", "--port", "80", "plain"}), " ")
	if want := "srv --token *** --api-key=*** GITHUB_TOKEN=*** --port 80 plain"; got != want {
		t.Errorf("args %q, want %q", got, want)
	}
	if got := maskURL("https://u:pw@h/mcp?api_key=s&x=1"); got != "https://u@h/mcp?api_key=***&x=1" {
		t.Errorf("url %q", got)
	}
	cfg := Server{URL: "https://u:pw123@h/mcp?api_key=abc123", Args: []string{"--token", "s3cr3t"},
		Env: map[string]string{"GITHUB_TOKEN": "ghp_x"}, Headers: map[string]string{"Authorization": "Bearer zzz"}}
	got = maskError(`Post "https://u:***@h/mcp?api_key=abc123": EOF; s3cr3t ghp_x Bearer zzz`, cfg, nil)
	if want := `Post "https://u:***@h/mcp?api_key=***": EOF; *** *** ***`; got != want {
		t.Errorf("maskError = %q, want %q", got, want)
	}
}

func TestArgs(t *testing.T) {
	args, err := Args(json.RawMessage(stubSchema))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, a := range args {
		got = append(got, fmt.Sprintf("%s:%s:%v:%v", a.Name, a.Type, a.Required, a.Flag))
	}
	want := []string{"query:string:true:false", "limit:integer:false:true", "exact:boolean:false:true",
		"filter:object:false:true", "tags:array:false:true", "author:string:true:false"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args %q\nwant %q", got, want)
	}
}

func TestCLI(t *testing.T) {
	args, _ := Args(json.RawMessage(stubSchema))
	parse := func(argv []string) (map[string]any, error) { return tools.ParseCLI("stub_search", args, argv, nil) }
	if u, want := tools.Usage("stub_search", args), "stub_search QUERY [--limit LIMIT] [--exact] [--filter JSON] [--tags JSON] AUTHOR"; u != want {
		t.Errorf("usage %q\nwant  %q", u, want)
	}
	got, err := parse([]string{"--limit", "5", "cats", "--exact", `--filter={"a":1}`, "--tags", `["x"]`, "me"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"query": "cats", "limit": 5, "exact": true, "filter": map[string]any{"a": 1.0}, "tags": []any{"x"}, "author": "me"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parsed %v\nwant   %v", got, want)
	}
	if got, err := parse([]string{"--", "--limit", "me"}); err != nil || got["query"] != "--limit" {
		t.Errorf("after --: %v %v", got, err)
	}
	for _, bad := range [][]string{{"cats"}, {"cats", "me", "extra"}, {"cats", "me", "--limit"}, {"cats", "me", "--tags", "[x"}} {
		if _, err := parse(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}
