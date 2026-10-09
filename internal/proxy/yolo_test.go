package proxy

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/mattn/go-runewidth"

	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/session"
)

// Only the user switches aish yolo: the agent's command during a request
// is refused as with aish model, a process in the background between
// requests is not the shell's foreground. The status says while it is on.
func TestYoloSwitch(t *testing.T) {
	p, _, _ := hosted(t, &scripted{})
	on := rpc.YoloParams{On: true}

	p.marker(Marker{Kind: "ask-start"})
	if _, err := call(t, p, rpc.MethodYolo, on); err == nil || err.Error() != "yolo is switched by the user, not by the assistant" {
		t.Errorf("during a request: %v", err)
	}
	p.mu.Lock()
	fg := p.fg
	p.fg = func() (int, error) { return syscall.Getpgrp() + 1, nil }
	p.mu.Unlock()
	if _, err := call(t, p, rpc.MethodYolo, on); err == nil || err.Error() != errYoloAsks.Error() {
		t.Errorf("during a request, from the background: %v", err)
	}
	p.marker(Marker{Kind: "cmd-end", Payload: "0;/tmp"})
	if _, err := call(t, p, rpc.MethodYolo, on); !errors.Is(err, errNotShell) {
		t.Errorf("from the background: %v", err)
	}
	if p.yoloOn() {
		t.Fatal("switched on by the assistant")
	}
	if text, _ := p.statusText(); strings.Contains(text, yoloMark) {
		t.Errorf("status with yolo off: %q", text)
	}

	p.mu.Lock()
	p.fg = fg
	p.mu.Unlock()
	yoloByUser(t, p)
	if text, _ := p.statusText(); text != "m · yolo" {
		t.Errorf("status with yolo on: %q", text)
	}
	if err := p.sess.Append(session.Entry{Kind: session.KindAssistant, Text: "a", InputTokens: 700}); err != nil {
		t.Fatal(err)
	}
	p.window = 1000
	text, color := p.statusText()
	if text != "700/1.0k 70% · m · yolo" {
		t.Errorf("status with tokens and yolo on: %q", text)
	}
	// Red, the rest as dim as before; as wide as the plain text.
	l := newInputLine(120, 24, text, color)
	p.paintYolo(l)
	if want := "700/1.0k 70% · m · " + yoloColor + yoloMark; l.text != want || l.w != runewidth.StringWidth(text) {
		t.Errorf("painted %q (%d columns), want %q", l.text, l.w, want)
	}
	if out := string(l.draw()); !strings.Contains(out, color+"700/1.0k") || !strings.HasSuffix(out, yoloColor+yoloMark+"\x1b[0m\x1b8") {
		t.Errorf("drawn %q", out)
	}

	if _, err := call(t, p, rpc.MethodYolo, rpc.YoloParams{}); err != nil {
		t.Fatalf("off by the user: %v", err)
	}
	if text, _ := p.statusText(); p.yoloOn() || text != "700/1.0k 70% · m" {
		t.Errorf("after yolo off: %v, status %q", p.yoloOn(), text)
	}
}

// Under yolo the agent's command that config.toml's [policy] denies goes
// to the shell, also after aish clear and aish new, which leave the switch
// on; with yolo off it is denied again.
func TestYoloRequest(t *testing.T) {
	p := configured(t, "[policy]\ndeny = [\"rm *\"]\n")
	prov := &scripted{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "bash", Args: json.RawMessage(`{"command":"rm x"}`)}}},
		{ToolCalls: []llm.ToolCall{{ID: "c2", Name: "bash", Args: json.RawMessage(`{"command":"rm x"}`)}}},
		{Text: "not allowed"},
	}}
	p.newProvider = func(config.Config) (llm.Provider, error) { return prov, nil }
	next := filepath.Join(p.run, "next.cmd")
	cwd := filepath.Join(os.Getenv("HOME"), "work")
	request := func() {
		t.Helper()
		p.marker(Marker{Kind: "ask-start"})
		if _, err := call(t, p, rpc.MethodAgentStart, rpc.AgentParams{Text: "remove x", Cwd: cwd}); err != nil {
			t.Fatal(err)
		}
	}

	yoloByUser(t, p)
	if _, err := call(t, p, rpc.MethodClear, rpc.ClearParams{}); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, p, rpc.MethodClear, rpc.ClearParams{Name: "work"}); err != nil {
		t.Fatal(err)
	}
	request()
	if b, _ := os.ReadFile(next); string(b) != "rm x" {
		t.Fatalf("under yolo, after clear and new: handed off %q", b)
	}
	if err := os.WriteFile(next, nil, 0o600); err != nil { // the shell takes it
		t.Fatal(err)
	}
	p.marker(Marker{Kind: "cmd-end", Payload: "130;" + cwd})

	if _, err := call(t, p, rpc.MethodYolo, rpc.YoloParams{}); err != nil {
		t.Fatal(err)
	}
	request()
	if b, _ := os.ReadFile(next); len(b) > 0 {
		t.Errorf("yolo off: handed off %q", b)
	}
	es := p.sess.Entries()
	if r := es[len(es)-2]; r.Kind != session.KindToolResult || r.ToolCallID != "c2" || !strings.HasPrefix(r.Output, `denied by policy: matches "rm *"`) {
		t.Errorf("yolo off: result %+v", r)
	}
}
