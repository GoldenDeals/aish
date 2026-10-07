package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/inebotov/aish/internal/tools"
)

// instructedStub is the config clientStubManager makes, with the
// instructions the stub gives as JSON: the same key, so the same cache.
func instructedStub(dir, instructions string) map[string]Server {
	exe, _ := os.Executable()
	return map[string]Server{"stub": {Command: exe, Env: map[string]string{
		"AISH_MCP_CLIENT_STUB": "1", "AISH_MCP_LOG": filepath.Join(dir, "log"), "AISH_MCP_INSTRUCTIONS": instructions,
	}}}
}

// The instructions of initialize come with the server's tools: in the
// listing, in the cache, from it to the next shell without a start, and on
// each tool as the agent gets it.
func TestInstructions(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "cache")
	ctx := context.Background()
	want := map[string]string{"stub": "Call work with care."}

	m := NewManager(instructedStub(dir, `"  Call work with care.\n"`), cache)
	t.Cleanup(m.Close)
	if res := m.List(ctx, false); len(res.Tools) != 0 || res.Instructions != nil {
		t.Fatalf("listed without starting: %+v", res)
	}
	if res := m.List(ctx, true); len(res.Tools) != 1 || !reflect.DeepEqual(res.Instructions, want) {
		t.Fatalf("listing: %+v", res)
	}
	var c cacheFile
	if b, err := os.ReadFile(filepath.Join(cache, "stub.json")); err != nil || json.Unmarshal(b, &c) != nil || c.Instructions != want["stub"] {
		t.Errorf("cache %+v, %v", c, err)
	}
	m.Close()

	m2 := NewManager(instructedStub(dir, `"  Call work with care.\n"`), cache)
	t.Cleanup(m2.Close)
	if res := m2.List(ctx, false); len(res.Tools) != 1 || !reflect.DeepEqual(res.Instructions, want) {
		t.Errorf("from the cache: %+v", res)
	}
	ts, problems := Local(ctx, m2, false)
	if len(ts) != 1 || len(problems) != 0 || tools.InstructionsOf(ts[0]) != want["stub"] || !tools.IsHidden(ts[0]) {
		t.Errorf("tools %+v, problems %q", ts, problems)
	}
	if got := logLines(dir); !reflect.DeepEqual(got, []string{"start"}) {
		t.Errorf("log %q: the cache started the server", got)
	}

	// A cache written before instructions were: the tools without them.
	old, _ := json.Marshal(map[string]any{"key": m2.servers[0].key, "tools": c.Tools})
	if err := os.WriteFile(filepath.Join(cache, "stub.json"), old, 0o600); err != nil {
		t.Fatal(err)
	}
	m3 := NewManager(instructedStub(dir, `"  Call work with care.\n"`), cache)
	t.Cleanup(m3.Close)
	if res := m3.List(ctx, false); len(res.Tools) != 1 || res.Instructions != nil {
		t.Errorf("old cache: %+v", res)
	}
}

// Instructions that are not a string are none; the server works all the
// same.
func TestInstructionsNotString(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(instructedStub(dir, `{"text":"x"}`), filepath.Join(dir, "cache"))
	t.Cleanup(m.Close)
	if res := m.List(context.Background(), true); len(res.Tools) != 1 || len(res.Errors) != 0 || res.Instructions != nil {
		t.Errorf("listing: %+v", res)
	}
}

// The tools that reach the agent through `aish` over the socket carry the
// instructions too.
func TestRemoteInstructions(t *testing.T) {
	b, _ := json.Marshal(ListResult{
		Tools:        []ToolInfo{{Name: "a_x", Server: "a", Expose: "deferred"}, {Name: "b_y", Server: "b", Expose: "tools"}},
		Instructions: map[string]string{"a": "Use x."},
	})
	var res ListResult
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatal(err)
	}
	ts, _ := convert(res, nil)
	if len(ts) != 2 || tools.InstructionsOf(ts[0]) != "Use x." || tools.InstructionsOf(ts[1]) != "" {
		t.Errorf("instructions %q, %q", tools.InstructionsOf(ts[0]), tools.InstructionsOf(ts[1]))
	}
	if !tools.IsHidden(ts[0]) || tools.IsHidden(ts[1]) {
		t.Error("deferred is hidden, tools is not")
	}
}

// expose is deferred, by default, or tools; commands is gone with the
// command wrappers.
func TestExpose(t *testing.T) {
	dir := t.TempDir()
	load := func(expose string) error {
		path := filepath.Join(dir, "mcp.yaml")
		y := "servers:\n  s:\n    command: x\n"
		if expose != "" {
			y += "    expose: " + expose + "\n"
		}
		if err := os.WriteFile(path, []byte(y), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := LoadConfig(path)
		return err
	}
	for _, e := range []string{"", "deferred", "tools"} {
		if err := load(e); err != nil {
			t.Errorf("expose %q: %v", e, err)
		}
	}
	for _, e := range []string{"commands", "all"} {
		if err := load(e); err == nil || !strings.Contains(err.Error(), "expose must be deferred or tools") {
			t.Errorf("expose %q: %v", e, err)
		}
	}
	m := NewManager(map[string]Server{"s": {Command: "x"}}, dir)
	if st := m.Status().Servers[0]; st.Expose != "deferred" {
		t.Errorf("default expose %q", st.Expose)
	}
}
