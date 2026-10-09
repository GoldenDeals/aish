package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/session"
)

// remembering answers each turn with its next reply and keeps the
// requests it was sent.
type remembering struct {
	mu       sync.Mutex
	replies  []*llm.Response
	requests []llm.Request
}

func (r *remembering) Name() string                                    { return "fake" }
func (r *remembering) Model() string                                   { return "m" }
func (r *remembering) Efforts() []string                               { return []string{"low", "high"} }
func (r *remembering) MaxTokens(string) int64                          { return 0 }
func (r *remembering) Models(context.Context) ([]llm.ModelInfo, error) { return nil, nil }
func (r *remembering) Complete(_ context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := len(r.requests)
	r.requests = append(r.requests, req)
	if n >= len(r.replies) {
		return nil, errors.New("no reply scripted")
	}
	if onText != nil {
		onText(r.replies[n].Text)
	}
	return r.replies[n], nil
}

// prefixes are what the requests sent before their messages, the system
// prompt and the tools: what the provider caches first.
func (r *remembering) prefixes(t *testing.T) []string {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, req := range r.requests {
		b, err := json.Marshal(req.Tools)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, req.System+"\x00"+string(b))
	}
	return out
}

// turnPrefixes are the prefixes the turns of the journal recorded.
func turnPrefixes(p *Proxy) []string {
	var out []string
	for _, e := range p.sess.Entries() {
		if e.Kind == session.KindAssistant {
			out = append(out, e.Prefix)
		}
	}
	return out
}

func remembered(p *Proxy, replies ...*llm.Response) *remembering {
	prov := &remembering{replies: replies}
	p.newProvider = func(config.Config) (llm.Provider, error) { return prov, nil }
	return prov
}

const uncachedLine = "[aish: the provider did not read this session from its cache"

// The first turn after aish apply-config with another system prompt
// writes the cache anew: that it read nothing is no news, and the next
// request is not told of it. A turn after it with the same prefix that
// reads nothing is.
func TestApplyConfigColdTurn(t *testing.T) {
	p := configured(t, "system_prompt = \"Be brief.\"\n")
	turn := func(in, cached int) *llm.Response {
		return &llm.Response{Text: "ok", InputTokens: in, CachedTokens: cached}
	}
	remembered(p, turn(60000, 0), turn(70000, 60000), turn(80000, 0), turn(90000, 0), turn(95000, 90000))
	out := p.out.(*terminal)
	ask(t, p, nil)
	ask(t, p, nil)
	rewrite(t, "system_prompt = \"Be thorough.\"\n")
	apply(t, p, nil)
	ask(t, p, nil) // a turn of one request, cold
	ask(t, p, nil)
	if strings.Contains(out.String(), uncachedLine) {
		t.Fatalf("a cold turn after apply-config is told of:\n%s", out.String())
	}
	got := turnPrefixes(p)
	if len(got) != 4 || got[0] != got[1] || got[1] == got[2] || got[2] != got[3] || got[0] == "" {
		t.Fatalf("turn prefixes %q", got)
	}
	ask(t, p, nil) // the last two turns sent one prefix: the last one should have read it
	if !strings.Contains(out.String(), uncachedLine) {
		t.Errorf("a cache not read is not told of:\n%s", out.String())
	}
}

// mcpServer is an MCP server over HTTP with a few tools and instructions,
// all named by name.
func mcpServer(t *testing.T, name string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&m) != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var result any
		switch m.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": "2025-06-18",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": name, "version": "1"},
				"instructions":    "Use " + name + " for " + name + " things.",
			}
		case "tools/list":
			var list []any
			for _, tool := range []string{"find", "get", "put", "drop"} {
				list = append(list, map[string]any{
					"name": tool, "description": tool + " in " + name,
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id": map[string]any{"type": "string"}, "query": map[string]any{"type": "string"},
							"limit": map[string]any{"type": "integer"}, "all": map[string]any{"type": "boolean"},
						},
						"required": []string{"id"},
					},
				})
			}
			result = map[string]any{"tools": list}
		default: // a notification
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// After aish apply-config with another system prompt, a policy with a
// hint and more MCP servers, the requests send one prefix, request after
// request: the cache the first of them writes is the next one's to read.
func TestApplyConfigPrefix(t *testing.T) {
	p := configured(t, "system_prompt = \"Be brief.\"\n")
	t.Setenv("XDG_CACHE_HOME", filepath.Join(os.Getenv("HOME"), "cache"))
	urls := map[string]string{}
	for _, n := range []string{"alpha", "beta", "gamma", "delta"} {
		urls[n] = mcpServer(t, n)
	}
	path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "aish", "mcp.yaml")
	if err := os.MkdirAll(filepath.Join(filepath.Dir(path), "policy"), 0o755); err != nil {
		t.Fatal(err)
	}
	servers := func(exposed map[string]string) {
		t.Helper()
		s := "servers:\n"
		for _, n := range []string{"alpha", "beta", "gamma", "delta"} {
			if expose, ok := exposed[n]; ok {
				s += fmt.Sprintf("  %s:\n    url: %s\n    expose: %s\n", n, urls[n], expose)
			}
		}
		if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	servers(map[string]string{"alpha": "tools"})
	mcpStarted(t, p, path)
	p.mcp.List(context.Background(), true)
	prov := remembered(p, oks(4)...)
	ask(t, p, nil)

	rewrite(t, "system_prompt = \"Be thorough.\"\n")
	servers(map[string]string{"alpha": "tools", "beta": "deferred", "gamma": "tools", "delta": "deferred"})
	cedar := "permit(principal, action, resource);\n" +
		"@hint(\"Do not remove files; move them to ~/trash.\")\n" +
		"forbid(principal, action == Action::\"run\", resource) when { context.program == \"rm\" };\n"
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "policy", "a.cedar"), []byte(cedar), 0o600); err != nil {
		t.Fatal(err)
	}
	apply(t, p, nil)
	// The servers new to the config start in the background: the
	// request that comes before they are up goes without them.
	p.mcp.List(context.Background(), true)
	for range 3 {
		ask(t, p, nil)
	}

	sent := prov.prefixes(t)
	if len(sent) != 4 {
		t.Fatalf("%d requests", len(sent))
	}
	for _, want := range []string{"Be thorough.", "Do not remove files", "Use beta for beta things.", "Use gamma for gamma things.",
		"beta_find", "delta_drop", `"Name":"gamma_put"`} {
		if !strings.Contains(sent[1], want) {
			t.Errorf("no %q in the prefix after apply-config:\n%s", want, sent[1])
		}
	}
	if sent[0] == sent[1] {
		t.Error("the prefix did not change with the config")
	}
	for i := 2; i < len(sent); i++ {
		if sent[i] != sent[1] {
			t.Errorf("request %d sent another prefix:\n%s\nafter\n%s", i+1, sent[i], sent[1])
		}
	}
	if got := turnPrefixes(p); len(got) != 4 || got[0] == got[1] || got[1] != got[2] || got[2] != got[3] {
		t.Errorf("turn prefixes %q", got)
	}
}
