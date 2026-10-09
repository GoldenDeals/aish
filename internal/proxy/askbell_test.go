package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/rpc"
)

// ask_bell rings the terminal's bell as the agent opens a question, the
// policy's Yes or No and the form of ask_user alike: before the question,
// and at once while the viewer holds the question itself till it closes.
// Off, nothing rings.
func TestAskBell(t *testing.T) {
	folds := []Fold{{Title: "❯ ls", Text: "a\r\n"}}
	for _, tc := range []struct {
		name string
		ask  func(context.Context, *ui)
		text string
	}{
		{"Yes or No", func(ctx context.Context, u *ui) { _, _ = u.Ask(ctx, "allow?") }, "allow?"},
		{"form", func(ctx context.Context, u *ui) { _, _ = u.Form(ctx, twoQuestions()) }, "Which approach?"},
	} {
		for _, on := range []bool{true, false} {
			for _, view := range []bool{false, true} {
				name := fmt.Sprintf("%s, ask_bell %v, viewer %v", tc.name, on, view)
				p, out := termProxy(t)
				p.mu.Lock()
				p.askBell = on
				if view {
					p.folds = folds
					p.openView(folds)
				}
				p.mu.Unlock()
				ctx, cancel := context.WithCancel(context.Background())
				done := make(chan struct{})
				go func() {
					defer close(done)
					tc.ask(ctx, &ui{p: p})
				}()
				waitOpen(t, p, func() bool { return p.ask != nil || p.form != nil })
				if view {
					if s := out.String(); strings.Contains(s, tc.text) || on != strings.Contains(s, "\a") {
						t.Errorf("%s: under the viewer %q", name, s)
					}
					p.key([]byte{ctrlO}) // the viewer closes, and the question shows
				}
				s := out.String()
				bell, q := strings.Index(s, "\a"), strings.Index(s, tc.text)
				switch {
				case q < 0:
					t.Errorf("%s: no question in %q", name, s)
				case on && (bell < 0 || bell > q || strings.Count(s, "\a") != 1):
					t.Errorf("%s: no bell before the question in %q", name, s)
				case !on && bell >= 0:
					t.Errorf("%s: a bell in %q", name, s)
				}
				cancel()
				<-done
			}
		}
	}
}

// The question of aish yolo rings no bell, ask_bell or not: the user has
// just run it.
func TestAskBellYolo(t *testing.T) {
	p, out, _ := hosted(t, &scripted{})
	p.mu.Lock()
	p.askBell = true
	p.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	res := askYolo(t, p, ctx)
	if s := out.String(); !strings.Contains(s, "Turn the checks off?") || strings.Contains(s, "\a") {
		t.Errorf("the question %q", s)
	}
	cancel()
	_ = answered(t, res)
}

// The proxy goes by ask_bell of the request's config: config.toml's top
// level, or the shell's profile laid over it.
func TestAskBellRequest(t *testing.T) {
	args := `{"questions":[{"question":"Which approach?","header":"Approach","options":[{"label":"Rewrite"},{"label":"Patch"}]}]}`
	for _, tc := range []struct {
		name, toml, profile string
		bell                bool
	}{
		{"off by default", "", "", false},
		{"on", "ask_bell = true\n", "", true},
		{"on, the profile's", "[profiles.p]\nask_bell = true\n", "p", true},
		{"on, the profile off", "ask_bell = true\n[profiles.p]\nask_bell = false\n", "p", false},
	} {
		prov := &scripted{replies: []*llm.Response{
			{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "ask_user", Args: json.RawMessage(args)}}},
			{Text: "going on without it"},
		}}
		p, out, cwd := hosted(t, prov)
		toml := "ask_timeout = \"50ms\"\n" + tc.toml
		if err := os.WriteFile(os.Getenv("AISH_CONFIG"), []byte(toml), 0o600); err != nil {
			t.Fatal(err)
		}
		// The proxy's own at start, the top level's: the request has the
		// shell's profile.
		top, err := config.LoadProfile("")
		if err != nil {
			t.Fatal(err)
		}
		p.mu.Lock()
		p.applyFields(top)
		p.profile = tc.profile
		p.mu.Unlock()
		p.size = func() (int, int) { return 80, 24 }
		p.marker(Marker{Kind: "ask-start"})
		if _, err := call(t, p, rpc.MethodAgentStart, rpc.AgentParams{Text: "make it nice", Cwd: cwd}); err != nil {
			t.Fatal(err)
		}
		s := out.String()
		bell, q := strings.Index(s, "\a"), strings.Index(s, "Which approach?")
		if q < 0 || tc.bell != (bell >= 0) || bell > q {
			t.Errorf("%s: terminal %q", tc.name, s)
		}
	}
}
