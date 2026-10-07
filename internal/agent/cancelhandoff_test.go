package agent

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/session"
	"github.com/inebotov/aish/internal/tools"
)

// cancelUI is a terminal where Ctrl+C comes as soon as a line with on in
// it is shown.
type cancelUI struct {
	*fakeUI
	on     string
	cancel context.CancelFunc
}

func (u *cancelUI) Write(b []byte) (int, error) {
	if strings.Contains(string(b), u.on) {
		u.cancel()
	}
	return u.fakeUI.Write(b)
}

// ranTool counts the times it is executed.
type ranTool struct {
	probeTool
	ran *int
}

func (t ranTool) Execute(ctx context.Context, ex tools.Exec, args map[string]any, live io.Writer) (string, error) {
	*t.ran++
	return t.probeTool.Execute(ctx, ex, args, live)
}

// Ctrl+C that comes after the pre-tool hook and the policy have answered,
// before the call runs: the command is not handed to the shell, the tool
// is not executed, the request ends interrupted, and the next request
// closes the call as interrupted, as any call Ctrl+C leaves.
func TestNoHandOffAfterCancel(t *testing.T) {
	for _, tc := range []struct{ tool, args, reply string }{
		{"bash", `{"command":"ls"}`, `{"args":{"command":"ls -l"}}`},
		{"remote", `{"what":"ls"}`, `{"args":{"what":"ls -l"}}`},
		{"probe", `{"what":"a"}`, `{"args":{"what":"b"}}`},
	} {
		prov := &fakeProvider{replies: []*llm.Response{
			{ToolCalls: []llm.ToolCall{toolCall("c1", tc.tool, tc.args)}},
			{Text: "ok"},
		}}
		a, j, sh, fui, cwd := newAgent(t, prov)
		ran := 0
		a.Tools.Add(remoteShell{probeTool{name: "remote"}})
		a.Tools.Add(ranTool{probeTool{name: "probe"}, &ran})
		a.Cfg.HooksDir = ""
		hook(t, a, "pre-tool", "h", "cat >/dev/null; echo '"+tc.reply+"'")
		ctx, cancel := context.WithCancel(context.Background())
		// The hook has answered: the policy checks the arguments it gave
		// once this line is out.
		a.UI = &cancelUI{fakeUI: fui, on: "arguments replaced by", cancel: cancel}

		if err := a.Start(ctx, "go", tools.Exec{Dir: cwd}); !errors.Is(err, context.Canceled) {
			t.Errorf("%s: request ended with %v", tc.tool, err)
		}
		if ctx.Err() == nil {
			t.Fatalf("%s: no Ctrl+C; terminal:\n%s", tc.tool, fui.String())
		}
		if len(sh.handed) != 0 || ran != 0 {
			t.Errorf("%s: handed off %q, executed %d times", tc.tool, sh.handed, ran)
		}
		if got := kinds(j.es); got != "user assistant" {
			t.Errorf("%s: journal after Ctrl+C: %s", tc.tool, got)
		}

		if err := a.Start(context.Background(), "go on", tools.Exec{Dir: cwd}); err != nil {
			t.Fatalf("%s: %v", tc.tool, err)
		}
		if got := kinds(j.es); got != "user assistant tool_result user assistant" {
			t.Fatalf("%s: journal %s", tc.tool, got)
		}
		r := j.es[2]
		if r.Kind != session.KindToolResult || r.ToolCallID != "c1" || !r.IsError || r.Output != "interrupted by the user" {
			t.Errorf("%s: result %+v", tc.tool, r)
		}
		if len(sh.handed) != 0 || ran != 0 {
			t.Errorf("%s: next request handed off %q, executed %d times", tc.tool, sh.handed, ran)
		}
	}
}
