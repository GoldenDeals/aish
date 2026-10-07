package proxy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/rpc"
)

// agentLine is the agent's line of calls as hide_work leaves it for the
// shell's command: open, the cursor at its end.
const agentLine = "\r⠋ Running 1 command…\x1b[K"

// A command the agent hid draws nothing, whatever fold_lines says, not
// even the end of its line; Ctrl+O has it, without output too.
func TestHiddenCommand(t *testing.T) {
	for _, limit := range []int{0, 3, -1} {
		for _, output := range []string{"a\r\nb\r\nc\r\nd\r\n", "a", ""} {
			p, out := statusProxy(t, 80)
			p.foldLines = limit
			u := &ui{p: p}
			u.Write([]byte(agentLine))
			u.HideCommand(nil)
			p.marker(Marker{Kind: "agent-start", Payload: "c1;seq 4"})
			p.output([]byte(output))
			if output != "" {
				if folds := p.viewFolds(); len(folds) != 1 || folds[0].Title != "❯ seq 4  (running)" || folds[0].Text != output {
					t.Errorf("fold_lines %d, %q: Ctrl+O while it runs %+v", limit, output, folds)
				}
			}
			p.marker(Marker{Kind: "agent-end", Payload: "c1;1;/tmp"})
			if got := out.String(); got != agentLine {
				t.Errorf("fold_lines %d, %q: drew %q", limit, output, strings.TrimPrefix(got, agentLine))
			}
			if want := []Fold{{Title: "❯ seq 4", Text: output}}; !slices.Equal(p.folds, want) {
				t.Errorf("fold_lines %d, %q: folds %+v", limit, output, p.folds)
			}
			if o := p.done["c1"]; o.Exit != 1 || o.Output != strings.TrimRight(strings.ReplaceAll(output, "\r", ""), "\n") {
				t.Errorf("fold_lines %d, %q: recorded %+v", limit, output, o)
			}
			// The agent goes on with its line; the prompt adds nothing.
			u.Write([]byte("\r● Ran 1 command\x1b[K\n"))
			before := out.String()
			p.marker(Marker{Kind: "cmd-end", Payload: "0;/tmp"})
			if got := strings.TrimPrefix(out.String(), before); strings.Contains(got, "interrupted") {
				t.Errorf("fold_lines %d, %q: the prompt drew %q", limit, output, got)
			}
		}
	}
}

// One command is hidden, not the next: it folds as fold_lines says.
func TestHiddenOneCommand(t *testing.T) {
	p, out := statusProxy(t, 80)
	u := &ui{p: p}
	u.HideCommand(nil)
	p.marker(Marker{Kind: "agent-start", Payload: "c1;ls"})
	p.output([]byte("a\r\n"))
	p.marker(Marker{Kind: "agent-end", Payload: "c1;0;/tmp"})
	if out.String() != "" {
		t.Fatalf("hidden command drew %q", out.String())
	}
	p.marker(Marker{Kind: "agent-start", Payload: "c2;ls"})
	p.output([]byte("a\r\n"))
	p.marker(Marker{Kind: "agent-end", Payload: "c2;0;/tmp"})
	if got := out.String(); !strings.Contains(got, "(1 line · ctrl+o to expand)") {
		t.Errorf("the next command drew %q", got)
	}
}

// A hidden command cut short, or never run, leaves the agent's line to the
// prompt, which cmd-end puts below it.
func TestHiddenCommandCut(t *testing.T) {
	const cut = "  " + dim + "(interrupted)" + reset + "\r\n"
	for _, tc := range []struct {
		name string
		run  func(p *Proxy)
	}{
		{"cut short", func(p *Proxy) {
			p.marker(Marker{Kind: "agent-start", Payload: "c1;sleep 9"})
			p.output([]byte("^C"))
		}},
		{"never run", func(*Proxy) {}},
		{"done, not resumed", func(p *Proxy) {
			p.marker(Marker{Kind: "agent-start", Payload: "c1;true"})
			p.marker(Marker{Kind: "agent-end", Payload: "c1;0;/tmp"})
		}},
	} {
		p, out := statusProxy(t, 80)
		u := &ui{p: p}
		u.Write([]byte(agentLine))
		u.HideCommand(nil)
		tc.run(p)
		p.marker(Marker{Kind: "cmd-end", Payload: "130;/tmp"})
		if got := strings.TrimPrefix(out.String(), agentLine); got != cut {
			t.Errorf("%s: cmd-end drew %q, want %q", tc.name, got, cut)
		}
		if p.hide || p.waits {
			t.Errorf("%s: hide %v, waits %v after the prompt", tc.name, p.hide, p.waits)
		}
	}
}

// A full-screen program is shown, below the agent's line.
func TestHiddenCommandFullScreen(t *testing.T) {
	p, out := statusProxy(t, 80)
	u := &ui{p: p}
	u.Write([]byte(agentLine))
	u.HideCommand(nil)
	p.marker(Marker{Kind: "agent-start", Payload: "c1;vim"})
	p.output([]byte("x\r\n"))
	p.output([]byte("\x1b[?1049hscreen"))
	if got := strings.TrimPrefix(out.String(), agentLine); got != "\r\nx\r\n\x1b[?1049hscreen" {
		t.Errorf("drew %q", got)
	}
}

// Hidden keeps a call for Ctrl+O and draws nothing.
func TestHiddenCall(t *testing.T) {
	p, out := statusProxy(t, 80)
	u := &ui{p: p}
	u.Hidden("⚙ read_file a.txt", "     1\ta\n")
	u.Hidden("⚙ write_file b.txt", "wrote 2 bytes")
	if out.String() != "" {
		t.Errorf("drew %q", out.String())
	}
	want := []Fold{{Title: "⚙ read_file a.txt", Text: "     1\ta\n"}, {Title: "⚙ write_file b.txt", Text: "wrote 2 bytes"}}
	if !slices.Equal(p.folds, want) {
		t.Errorf("folds %+v", p.folds)
	}
}

// The agent in the proxy with hide_work: a file read and a command run end
// as one line above the answer, and Ctrl+O has both.
func TestHideWorkRequest(t *testing.T) {
	prov := &scripted{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{
			{ID: "c1", Name: "read_file", Args: json.RawMessage(`{"path":"a.txt"}`)},
			{ID: "c2", Name: "bash", Args: json.RawMessage(`{"command":"ls"}`)},
		}},
		{Text: "one file"},
	}}
	p, out, cwd := hosted(t, prov)
	p.size = func() (int, int) { return 80, 24 }
	if err := os.WriteFile(os.Getenv("AISH_CONFIG"), []byte("hide_work = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "a.txt"), []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p.marker(Marker{Kind: "ask-start"})
	if _, err := call(t, p, rpc.MethodAgentStart, rpc.AgentParams{Text: "look", Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	if rows := screenRows(out.String()); len(rows) != 1 || !strings.HasSuffix(rows[0], " Reading 1 file, running 1 command…") {
		t.Fatalf("handed off: %q", rows)
	}
	before := out.String()
	p.marker(Marker{Kind: "agent-start", Payload: "c2;ls"})
	p.output([]byte("a.txt\r\n"))
	// The agent's line turns while the command runs, and nothing else is
	// drawn of it.
	time.Sleep(4 * spinTick)
	p.marker(Marker{Kind: "agent-end", Payload: "c2;0;" + cwd})
	turned := regexp.MustCompile(`\r\x1b\[36m(.) \x1b\[2mReading 1 file, running 1 command…\x1b\[0m\x1b\[K`)
	drawn := strings.TrimPrefix(out.String(), before)
	frames := map[string]bool{}
	for _, m := range turned.FindAllStringSubmatch(drawn, -1) {
		frames[m[1]] = true
	}
	if len(frames) < 2 || turned.ReplaceAllString(drawn, "") != "" {
		t.Errorf("the command drew %q", drawn)
	}
	if _, err := call(t, p, rpc.MethodAgentResume, rpc.AgentParams{ID: "c2", RC: 0, Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	p.marker(Marker{Kind: "cmd-end", Payload: "0;" + cwd})
	want := []string{"● Read 1 file, ran 1 command  (ctrl+o to expand)", "one file", ""}
	if got := screenRows(out.String()); !slices.Equal(got, want) {
		t.Errorf("screen %q, want %q", got, want)
	}
	if s := out.String(); strings.Contains(s, "⚙") || strings.Contains(s, "❯") {
		t.Errorf("a call drawn: %q", s)
	}
	if len(p.folds) != 2 || p.folds[0].Title != "⚙ read_file a.txt" || p.folds[1].Title != "❯ ls" || p.folds[1].Text != "a.txt\r\n" {
		t.Errorf("folds %+v", p.folds)
	}
	if got := journalKinds(p.sess); got != "user assistant tool_result tool_result assistant" {
		t.Errorf("journal %s", got)
	}
}

// spinLine is the agent's line of calls as HideCommand gets it to keep
// turning: n frames after agentLine's, with the columns it is given.
func spinLine(n, cols int) string {
	frames := []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")
	return fmt.Sprintf("%c Running 1 command… %d", frames[n%len(frames)], cols)
}

// spinDraws are the frames of spinLine drawn in place, the frame captured.
var spinDraws = regexp.MustCompile(`\r(.) Running 1 command… 80\x1b\[K`)

// spinning is statusProxy with the agent's line drawn and its command,
// hidden, handed to the shell, which runs it.
func spinning(t *testing.T) (*Proxy, *terminal, *ui) {
	t.Helper()
	p, out := statusProxy(t, 80)
	t.Cleanup(func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.stopSpin()
	})
	u := &ui{p: p}
	u.Write([]byte(agentLine))
	u.HideCommand(spinLine)
	p.marker(Marker{Kind: "agent-start", Payload: "c1;sleep 5"})
	return p, out, u
}

func spinStopped(p *Proxy) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.spin == nil
}

// While the hidden command runs, the proxy turns the agent's line: frames
// in its place and nothing else, none after agent-end.
func TestHiddenCommandSpins(t *testing.T) {
	p, out, _ := spinning(t)
	p.output([]byte("a\r\n"))
	time.Sleep(500 * time.Millisecond)
	drawn := strings.TrimPrefix(out.String(), agentLine)
	frames := map[string]bool{}
	for _, m := range spinDraws.FindAllStringSubmatch(drawn, -1) {
		frames[m[1]] = true
	}
	if len(frames) < 3 || spinDraws.ReplaceAllString(drawn, "") != "" {
		t.Errorf("%d frames in 500ms: %q", len(frames), drawn)
	}
	p.marker(Marker{Kind: "agent-end", Payload: "c1;0;/tmp"})
	ended := out.String()
	time.Sleep(3 * spinTick)
	if got := strings.TrimPrefix(out.String(), ended); got != "" || !spinStopped(p) {
		t.Errorf("after agent-end: %q", got)
	}
}

// The agent writing takes its line back, the command cut short leaves it
// to the prompt: either stops it turning, and the prompt's word goes after
// the last frame.
func TestHiddenCommandSpinStops(t *testing.T) {
	_, _, u := spinning(t)
	u.Write([]byte("\r● Ran 1 command\x1b[K\n"))
	if !spinStopped(u.p) {
		t.Error("turning after the agent wrote")
	}

	p, out, _ := spinning(t)
	time.Sleep(3 * spinTick)
	p.output([]byte("^C"))
	p.marker(Marker{Kind: "cmd-end", Payload: "130;/tmp"})
	ended := out.String()
	rows := screenRows(ended)
	if len(rows) != 2 || !spinDraws.MatchString(ended) || !strings.HasSuffix(rows[0], " Running 1 command… 80  (interrupted)") {
		t.Errorf("cut short: %q", ended)
	}
	time.Sleep(3 * spinTick)
	if got := strings.TrimPrefix(out.String(), ended); got != "" || !spinStopped(p) {
		t.Errorf("after the prompt: %q", got)
	}
}

// The viewer has the screen: no frame goes there, nor waits for it to
// close; the line turns again once it is closed. A full-screen program
// stops it: what the command prints after it shows below the line.
func TestHiddenCommandSpinScreen(t *testing.T) {
	p, out, _ := spinning(t)
	p.output([]byte("a\r\n"))
	if b := p.key([]byte{ctrlO}); len(b) != 0 {
		t.Fatalf("Ctrl+O went to the shell: %q", b)
	}
	opened := out.String()
	time.Sleep(3 * spinTick)
	p.mu.Lock()
	viewing, held := p.view != nil, string(p.held)
	p.mu.Unlock()
	if got := strings.TrimPrefix(out.String(), opened); !viewing || got != "" || held != "" {
		t.Errorf("viewer open %v: drew %q, held %q", viewing, got, held)
	}
	p.key([]byte("q"))
	closed := out.String()
	time.Sleep(3 * spinTick)
	if got := strings.TrimPrefix(out.String(), closed); !spinDraws.MatchString(got) {
		t.Errorf("after the viewer: %q", got)
	}

	p, out, _ = spinning(t)
	p.output([]byte("\x1b[?1049hscreen"))
	time.Sleep(3 * spinTick)
	got := out.String()
	if i := strings.Index(got, "\r\n\x1b[?1049hscreen"); i < 0 || got[i:] != "\r\n\x1b[?1049hscreen" || !spinStopped(p) {
		t.Errorf("full-screen program: %q", got)
	}
}
