package proxy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/inebotov/aish/internal/config"
	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/rpc"
)

// bgHost starts its subagent in the background, which works until its
// context ends: started and stopped tell when.
type bgHost struct {
	scripted
	once             [2]sync.Once
	started, stopped chan struct{}
}

func (h *bgHost) Complete(ctx context.Context, req llm.Request, onText func(string)) (*llm.Response, error) {
	if strings.Contains(req.System, "# Subagent") {
		h.once[0].Do(func() { close(h.started) })
		<-ctx.Done()
		h.once[1].Do(func() { close(h.stopped) })
		return nil, ctx.Err()
	}
	h.mu.Lock()
	n := h.calls
	h.calls++
	h.mu.Unlock()
	if n > 0 {
		return &llm.Response{Text: "started it"}, nil
	}
	args := `{"tasks":[{"agent":"helper","prompt":"work"}],"background":true}`
	return &llm.Response{ToolCalls: []llm.ToolCall{{ID: "t1", Name: "task", Args: json.RawMessage(args)}}}, nil
}

// The subagents in the background belong to the session: clear and resume
// stop them, and return once they are stopped; a clear refused does not.
func TestSessionChangeStopsBackground(t *testing.T) {
	for _, method := range []string{rpc.MethodClear, rpc.MethodResume} {
		t.Run(method, func(t *testing.T) {
			h := &bgHost{started: make(chan struct{}), stopped: make(chan struct{})}
			p, _, cwd := hosted(t, &h.scripted)
			p.newProvider = func(config.Config) (llm.Provider, error) { return h, nil }
			dir := filepath.Join(cwd, ".claude", "agents")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "helper.md"), []byte("---\nname: helper\ndescription: Helps\n---\nHelp.\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(p.stopBackground)

			p.marker(Marker{Kind: "ask-start"})
			if _, err := call(t, p, rpc.MethodAgentStart, rpc.AgentParams{Text: "start it", Cwd: cwd}); err != nil {
				t.Fatal(err)
			}
			<-h.started
			if _, err := call(t, p, rpc.MethodClear, rpc.ClearParams{}); err == nil {
				t.Fatal("the assistant cleared the session")
			}
			p.marker(Marker{Kind: "cmd-end", Payload: "0;" + cwd})
			select {
			case <-h.stopped:
				t.Fatal("the subagent stopped with the request, or with a clear refused")
			default:
			}

			var err error
			switch method {
			case rpc.MethodClear:
				_, err = call(t, p, method, rpc.ClearParams{})
			case rpc.MethodResume:
				// Not session.New: its id would be this one's, made the same second.
				const other = "20000101-000000-1"
				if err := os.WriteFile(filepath.Join(p.sess.Dir(), other+".jsonl"), []byte(`{"kind":"user","text":"hi"}`+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				_, err = call(t, p, method, rpc.ResumeParams{ID: other})
			}
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-h.stopped:
			default:
				t.Errorf("%s returned with the subagent at work", method)
			}
		})
	}
}
