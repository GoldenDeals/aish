package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GoldenDeals/aish-core/contracts/llm"
	"github.com/GoldenDeals/aish-core/contracts/tool"
	corebus "github.com/GoldenDeals/aish-core/internal/core/bus"
	"github.com/GoldenDeals/aish-core/pkg/bus"
	"github.com/GoldenDeals/aish-core/pkg/event"
	"github.com/GoldenDeals/aish-core/pkg/tx"
)

type cfg map[string]any

func (c cfg) Map() map[string]any { return c }

func recv[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
		panic("unreachable")
	}
}

// TestMain runs the test binary as an MCP server over stdio when TestCalls starts it.
func TestMain(m *testing.M) {
	if os.Getenv("AISH_TEST_MCP") == "" {
		os.Exit(m.Run())
	}
	if err := echoServer().Run(context.Background(), &sdk.StdioTransport{}); err != nil {
		os.Exit(1)
	}
}

// echoServer has an echo.it tool that answers with its text, fails on "fail" and waits for cancellation on "wait".
func echoServer() *sdk.Server {
	srv := sdk.NewServer(&sdk.Implementation{Name: "t", Version: "v0"}, nil)
	sdk.AddTool(srv, &sdk.Tool{Name: "echo.it", Description: "echo"},
		func(ctx context.Context, _ *sdk.CallToolRequest, in struct {
			Text string `json:"text"`
		}) (*sdk.CallToolResult, any, error) {
			if in.Text == "wait" {
				<-ctx.Done()
				return nil, nil, ctx.Err()
			}
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: in.Text}}, IsError: in.Text == "fail"}, nil, nil
		})
	return srv
}

func TestCalls(t *testing.T) {
	h := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return echoServer() }, nil)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "secret" {
			http.Error(w, "no key", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	}))
	defer remote.Close()
	for name, srv := range map[string]map[string]any{
		"stdio": {"command": os.Args[0], "env": map[string]any{"AISH_TEST_MCP": 1}},
		"http":  {"url": remote.URL, "headers": map[string]any{"x-api-key": map[string]any{"command": "echo secret; echo meta"}}},
	} {
		t.Run(name, func(t *testing.T) { testCalls(t, srv) })
	}
}

func testCalls(t *testing.T, server map[string]any) {
	b := corebus.New()
	specs, results := make(chan tool.Spec, 10), make(chan tool.Output, 10)
	bus.Sync(b, 0, event.ByName(tool.Declare), func(_ context.Context, _ tx.Tx, ev event.Event[tool.Spec]) error {
		specs <- ev.Data()
		return nil
	})
	bus.Sync(b, 0, event.ByName(tool.Result), func(_ context.Context, _ tx.Tx, ev event.Event[tool.Output]) error {
		results <- ev.Data()
		return nil
	})
	m := &mod{}
	if err := m.Init(b, cfg{"servers": map[string]any{"t": server}}); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer m.Stop(context.Background())
	if s := recv(t, specs); s.Name != "mcp__t__echo_it" || s.Description != "echo" || !strings.Contains(string(s.Schema), `"text"`) {
		t.Errorf("spec = %+v, %s", s, s.Schema)
	}

	call := func(ctx context.Context, input string) tool.Output {
		t.Helper()
		if err := b.Dispatch(ctx, event.New(tool.Call, tool.Use{ID: "1", Name: "mcp__t__echo_it", Input: []byte(input)})); err != nil {
			t.Fatal(err)
		}
		return recv(t, results)
	}
	if r := call(context.Background(), `{"text":"hi"}`); r != (tool.Output{ID: "1", Text: "hi"}) {
		t.Errorf("hi: %+v", r)
	}
	if r := call(context.Background(), `{"text":"fail"}`); r != (tool.Output{ID: "1", Text: "fail", Error: true}) {
		t.Errorf("fail: %+v", r)
	}
	ctx, stop := llm.WithTurn(context.Background())
	time.AfterFunc(50*time.Millisecond, stop)
	if r := call(ctx, `{"text":"wait"}`); !r.Error {
		t.Errorf("stopped turn: %+v", r)
	}
}

func TestConfig(t *testing.T) {
	for _, c := range []any{
		"fs",
		map[string]any{"fs": "npx"},
		map[string]any{"fs": map[string]any{}},
		map[string]any{"fs": map[string]any{"command": "npx", "url": "http://x"}},
		map[string]any{"fs": map[string]any{"command": "npx", "args": "-y"}},
		map[string]any{"fs": map[string]any{"url": "http://x", "headers": []any{"a"}}},
		map[string]any{"f.s": map[string]any{"command": "npx"}},
		map[string]any{"fs": map[string]any{"url": "http://x", "headers": map[string]any{"k": map[string]any{"command": "exit 1"}}}},
		map[string]any{"fs": map[string]any{"url": "http://x", "headers": map[string]any{"k": map[string]any{"command": "true"}}}},
	} {
		if err := (&mod{}).Init(corebus.New(), cfg{"servers": c}); err == nil {
			t.Errorf("servers %v are taken", c)
		}
	}
	m := &mod{}
	err := m.Init(corebus.New(), cfg{"servers": map[string]any{
		"fs": map[string]any{"command": "npx", "args": []any{"-y", 1}, "env": map[string]any{"PORT": 8080, "KEY": map[string]any{"command": "printf k"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	cmd := m.servers[0].transport.(*sdk.CommandTransport).Command
	if !slices.Equal(cmd.Args, []string{"npx", "-y", "1"}) || !slices.Contains(cmd.Env, "PORT=8080") || !slices.Contains(cmd.Env, "KEY=k") || len(cmd.Env) < 3 {
		t.Errorf("command %v with env %v", cmd.Args, cmd.Env)
	}
}
