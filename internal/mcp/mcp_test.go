package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/inebotov/aish/internal/tools"
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

func TestManager(t *testing.T) {
	dir := t.TempDir()
	cache, starts, bin := filepath.Join(dir, "cache"), filepath.Join(dir, "starts"), filepath.Join(dir, "bin")
	os.Mkdir(bin, 0o700)
	// A command named like one of the tools: its wrapper must not shadow it.
	path := filepath.Join(dir, "path")
	os.Mkdir(path, 0o700)
	os.WriteFile(filepath.Join(path, "stub_echo"), []byte("#!/bin/sh\n"), 0o755)
	t.Setenv("PATH", path+":"+os.Getenv("PATH"))

	ctx := context.Background()
	m := stubManager(t, cache, starts)
	m.Bin, m.Self = bin, "/x/aish"
	if res := m.List(ctx, false); len(res.Tools) != 0 {
		t.Fatalf("listed without starting: %+v", res)
	}
	res := m.List(ctx, true)
	var names []string
	for _, ti := range res.Tools {
		names = append(names, ti.Name)
	}
	if want := []string{"stub_search", "stub_echo", "stub_crash"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("tools %q, want %q", names, want)
	}
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0], "stub_echo: not a command, it would shadow") {
		t.Errorf("errors %q", res.Errors)
	}
	if b, err := os.ReadFile(filepath.Join(bin, "stub_search")); err != nil || !strings.Contains(string(b), `exec "/x/aish" tool stub_search "$@"`) {
		t.Errorf("wrapper: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(bin, "stub_echo")); err == nil {
		t.Error("wrapper shadows a command")
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
}

func TestBrokenServer(t *testing.T) {
	m := NewManager(map[string]Server{"bad": {Command: "/nonexistent/server"}}, t.TempDir())
	defer m.Close()
	res := m.List(context.Background(), true)
	if len(res.Tools) != 0 || len(res.Errors) != 1 || !strings.HasPrefix(res.Errors[0], "bad: ") {
		t.Fatalf("%+v", res)
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
	tool := tools.Tool{Name: "stub_search", Args: args}
	if u, want := tool.Usage(), "stub_search QUERY [--limit LIMIT] [--exact] [--filter JSON] [--tags JSON] AUTHOR"; u != want {
		t.Errorf("usage %q\nwant  %q", u, want)
	}
	got, err := tool.ParseCLI([]string{"--limit", "5", "cats", "--exact", `--filter={"a":1}`, "--tags", `["x"]`, "me"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"query": "cats", "limit": 5, "exact": true, "filter": map[string]any{"a": 1.0}, "tags": []any{"x"}, "author": "me"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parsed %v\nwant   %v", got, want)
	}
	if got, err := tool.ParseCLI([]string{"--", "--limit", "me"}, nil); err != nil || got["query"] != "--limit" {
		t.Errorf("after --: %v %v", got, err)
	}
	for _, bad := range [][]string{{"cats"}, {"cats", "me", "extra"}, {"cats", "me", "--limit"}, {"cats", "me", "--tags", "[x"}} {
		if _, err := tool.ParseCLI(bad, nil); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}
