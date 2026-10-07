package agent

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"

	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/policy"
	"github.com/inebotov/aish/internal/rpc"
	"github.com/inebotov/aish/internal/skills"
	"github.com/inebotov/aish/internal/tools"
)

// hiderUI is a terminal that keeps what hide_work does not draw.
type hiderUI struct {
	*fakeUI
	hidden   []string // titles, in the order kept
	texts    []string
	hideCmds int
	lines    []func(n, cols int) string // to keep turning, by HideCommand
	// keptAt is how much was written when each call was kept.
	keptAt []int
}

func (u *hiderUI) Hidden(title, text string) {
	u.hidden = append(u.hidden, title)
	u.texts = append(u.texts, text)
	u.keptAt = append(u.keptAt, u.Len())
}
func (u *hiderUI) HideCommand(line func(n, cols int) string) {
	u.hideCmds++
	u.lines = append(u.lines, line)
}

// hidingAgent is newAgent with hide_work on, on a terminal 80 columns wide
// that keeps the calls.
func hidingAgent(t *testing.T, prov *fakeProvider) (*Agent, *fakeJournal, *fakeShell, *hiderUI, string) {
	t.Helper()
	a, j, sh, ui, cwd := newAgent(t, prov)
	ui.cols = 80
	h := &hiderUI{fakeUI: ui}
	a.UI, a.Cfg.HideWork = h, true
	for _, f := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(cwd, f), []byte(f+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return a, j, sh, h, cwd
}

var csi = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

func stripCSI(s string) string { return csi.ReplaceAllString(s, "") }

// screenOf is what a terminal shows of out, row by row: enough of one for
// \r, \n and erasing to the end of the line; other sequences show nothing.
func screenOf(out string) []string {
	rows := [][]rune{nil}
	row, col := 0, 0
	rs := []rune(out)
	for i := 0; i < len(rs); i++ {
		switch c := rs[i]; c {
		case '\r':
			col = 0
		case '\n': // the proxy's UI gives it a carriage return
			col = 0
			if row++; row == len(rows) {
				rows = append(rows, nil)
			}
		case 0x1b:
			j := i + 2
			for j < len(rs) && (rs[j] < 0x40 || rs[j] > 0x7e) {
				j++
			}
			if j < len(rs) && rs[j] == 'K' && j == i+2 && col < len(rows[row]) {
				rows[row] = rows[row][:col]
			}
			i = j
		default:
			for len(rows[row]) <= col {
				rows[row] = append(rows[row], ' ')
			}
			rows[row][col] = c
			col++
		}
	}
	out2 := make([]string, len(rows))
	for i, r := range rows {
		out2[i] = strings.TrimRight(string(r), " ")
	}
	return out2
}

func TestWorkLabel(t *testing.T) {
	g := &workGroup{}
	if g.Label(true) != "" || g.Label(false) != "" {
		t.Fatalf("empty group: %q", g.Label(false))
	}
	for _, k := range []workKind{workRead, workRun, workRead, workRead, workRun} {
		g.add(k)
	}
	if got := g.Label(true); got != "Reading 3 files, running 2 commands" {
		t.Errorf("active %q", got)
	}
	if got := g.Label(false); got != "Read 3 files, ran 2 commands" {
		t.Errorf("done %q", got)
	}
	g.drop(workRun)
	g.drop(workRun)
	g.drop(workEdit) // never counted
	if got := g.Label(false); got != "Read 3 files" {
		t.Errorf("after drop %q", got)
	}

	one := &workGroup{}
	for _, k := range []workKind{workEdit, workSkill, workTool, workRun, workRead} {
		one.add(k)
	}
	if got := one.Label(false); got != "Edited 1 file, used 1 skill, called 1 tool, ran 1 command, read 1 file" {
		t.Errorf("one of each, done: %q", got)
	}
	if got := one.Label(true); got != "Editing 1 file, using 1 skill, calling 1 tool, running 1 command, reading 1 file" {
		t.Errorf("one of each, active: %q", got)
	}

	// Never wider than the terminal less its last column; the hint goes
	// first, then the end of the label.
	for _, cols := range []int{8, 20, 40, 79, 120} {
		done := stripCSI(one.done(cols))
		if w := runewidth.StringWidth(done); w > cols-1 {
			t.Errorf("%d columns: done %q is %d wide", cols, done, w)
		}
		if strings.HasSuffix(done, workHint) != (cols >= 120) {
			t.Errorf("%d columns: done %q", cols, done)
		}
		active := stripCSI(one.active("⠋", cols))
		if w := runewidth.StringWidth(active); w > cols-1 || !strings.HasSuffix(active, "…") {
			t.Errorf("%d columns: active %q is %d wide", cols, active, w)
		}
	}
	if got := stripCSI(g.done(80)); got != "● Read 3 files"+workHint {
		t.Errorf("done %q", got)
	}
}

// namedTool is a tool of any name, a server's or streaming.
type namedTool struct {
	name, server string
	stream       bool
}

func (t namedTool) Name() string         { return t.name }
func (namedTool) Desc() string           { return "" }
func (namedTool) Args() []tools.Arg      { return nil }
func (namedTool) Schema() map[string]any { return nil }
func (t namedTool) Server() string       { return t.server }
func (t namedTool) Streaming() bool      { return t.stream }
func (namedTool) Execute(context.Context, tools.Exec, map[string]any, io.Writer) (string, error) {
	return "", nil
}

func TestKindOf(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello"), []byte("#!/bin/sh\n# aish:desc Say hello\necho hello\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	reg := tools.Load(dir)
	for name, want := range map[string]workKind{
		"read_file": workRead, "write_file": workEdit, "edit_file": workEdit, "bash": workRun,
		"ask_user": workTool, "hello": workTool,
	} {
		tl, ok := reg.Get(name)
		if !ok {
			t.Fatalf("no tool %s", name)
		}
		if got := kindOf(tl); got != want {
			t.Errorf("%s: %v, want %v", name, got, want)
		}
	}
	for _, tc := range []struct {
		t    tools.Tool
		want workKind
	}{
		{skills.Skill{Name: "deploy"}.Tool(), workSkill},
		{namedTool{name: "read_file", server: "fs"}, workTool}, // an MCP server's
		{namedTool{name: "edit_file", stream: true}, workTool}, // a user's
		{namedTool{name: "query", server: "db"}, workTool},
		{&bgTool{name: taskWait}, workTool},
		{&bgTool{name: taskResult}, workTool},
		{searchTool{}, workTool},
	} {
		if got := kindOf(tc.t); got != tc.want {
			t.Errorf("%T %s: %v, want %v", tc.t, tc.t.Name(), got, tc.want)
		}
	}
}

// Three files read and a command run show as one line, redrawn in place:
// no line of a call, each call kept by the UI, the line going on after
// Resume and ending above the reply.
func TestHideWorkGroup(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{
			toolCall("c1", "read_file", `{"path":"a.txt"}`),
			toolCall("c2", "read_file", `{"path":"b.txt"}`),
			toolCall("c3", "read_file", `{"path":"c.txt"}`),
			toolCall("c4", "bash", `{"command":"ls"}`),
		}},
		{Text: "three notes"},
	}}
	a, j, sh, ui, cwd := hidingAgent(t, prov)
	if err := a.Start(context.Background(), "read them", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sh.handed, []string{"c4\x00ls"}) || ui.hideCmds != 1 {
		t.Fatalf("handed %q, hidden commands %d", sh.handed, ui.hideCmds)
	}
	rows := screenOf(ui.String())
	if len(rows) != 1 || !strings.HasSuffix(rows[0], " Reading 3 files, running 1 command…") {
		t.Errorf("while the command runs: %q", rows)
	}

	sh.outputs["c4"] = rpc.Output{Output: "a.txt\n", Cwd: cwd}
	if err := a.Resume(context.Background(), "c4", 0, tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	out := ui.String()
	want := []string{"● Read 3 files, ran 1 command" + workHint, "three notes", ""}
	if got := screenOf(out); !slices.Equal(got, want) {
		t.Errorf("screen %q, want %q", got, want)
	}
	if strings.Contains(out, "⚙") || strings.Contains(out, "❯") {
		t.Errorf("a call shown: %q", out)
	}
	if want := []string{"⚙ read_file a.txt", "⚙ read_file b.txt", "⚙ read_file c.txt"}; !slices.Equal(ui.hidden, want) || ui.texts[1] != "     1\tb.txt\n" {
		t.Errorf("kept %q %q", ui.hidden, ui.texts)
	}
	if len(ui.folds) != 0 || len(ui.lives) != 0 || len(ui.at) != 0 {
		t.Errorf("folds %q, lives %q, at %v", ui.folds, ui.lives, ui.at)
	}
	if got := kinds(j.es); got != "user assistant tool_result tool_result tool_result tool_result assistant" {
		t.Errorf("journal %s", got)
	}
	if a.work != nil || a.UI != ui {
		t.Errorf("the request is over, the UI still wrapped")
	}
}

// A denial and an error are shown as without hide_work and end the group;
// the next call begins another.
func TestHideWorkShown(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{
			toolCall("c1", "read_file", `{"path":"a.txt"}`),
			toolCall("c2", "bash", `{"command":"rm -rf x"}`),
			toolCall("c3", "read_file", `{"path":"b.txt"}`),
			toolCall("c4", "read_file", `{"path":"missing.txt"}`),
			toolCall("c5", "read_file", `{"path":"c.txt"}`),
		}},
		{Text: "ok"},
	}}
	a, _, sh, ui, cwd := hidingAgent(t, prov)
	pol, err := policy.Load(context.Background(), "", policy.Rules{Deny: []string{"rm *"}})
	if err != nil {
		t.Fatal(err)
	}
	a.Policy = pol
	if err := a.Start(context.Background(), "go", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	got := screenOf(ui.String())
	want := []string{
		"● Read 1 file" + workHint,
		"❯ rm -rf x",
		`  ✗ denied by policy: matches "rm *"`,
		"● Read 1 file" + workHint,
		"⚙ read_file missing.txt",
		"  ✗ …",
		"● Read 1 file" + workHint,
		"ok",
		"",
	}
	if len(got) == len(want) && strings.HasPrefix(got[5], "  ✗ ") {
		got[5] = "  ✗ …"
	}
	if !slices.Equal(got, want) {
		t.Errorf("screen\n%q, want\n%q", got, want)
	}
	if len(sh.handed) != 0 || len(ui.hidden) != 3 {
		t.Errorf("handed %q, kept %q", sh.handed, ui.hidden)
	}
}

// A call the user was asked about, and ask_user, are not hidden: they end
// the group and show as they would.
func TestHideWorkAsked(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{
			toolCall("c1", "read_file", `{"path":"a.txt"}`),
			toolCall("c2", "bash", `{"command":"git push"}`),
		}},
	}}
	a, _, sh, ui, cwd := hidingAgent(t, prov)
	pol, err := policy.Load(context.Background(), "", policy.Rules{Ask: []string{"git push*"}})
	if err != nil {
		t.Fatal(err)
	}
	a.Policy, ui.answer = pol, "y"
	if err := a.Start(context.Background(), "push", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"● Read 1 file" + workHint, "❯ git push", ""}; !slices.Equal(screenOf(ui.String()), want) {
		t.Errorf("screen %q, want %q", screenOf(ui.String()), want)
	}
	if len(ui.asked) != 1 || len(sh.handed) != 1 || ui.hideCmds != 0 {
		t.Errorf("asked %q, handed %q, hidden commands %d", ui.asked, sh.handed, ui.hideCmds)
	}
	if a.work != nil || a.UI != ui {
		t.Errorf("a command shown leaves no group waiting")
	}

	prov = &fakeProvider{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{
			toolCall("c1", "read_file", `{"path":"a.txt"}`),
			toolCall("c2", "ask_user", askArgs),
		}},
		{Text: "ok"},
	}}
	a, _, _, ui, cwd = hidingAgent(t, prov)
	ui.form = func(context.Context, []Question) ([]Answer, error) {
		return []Answer{{Picked: []string{"Patch"}}, {Picked: []string{"Colors"}}}, nil
	}
	if err := a.Start(context.Background(), "make it nice", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	rows := screenOf(ui.String())
	if len(rows) < 3 || rows[0] != "● Read 1 file"+workHint || rows[1] != "⚙ ask_user Approach, Features" || !slices.Contains(rows, "ok") {
		t.Errorf("screen %q", rows)
	}
	if len(ui.forms) != 1 || len(ui.hidden) != 1 {
		t.Errorf("forms %d, kept %q", len(ui.forms), ui.hidden)
	}
}

// spinnerDraws are what the spinner prints, as many times as time allows.
var spinnerDraws = regexp.MustCompile(`\r\x1b\[\?25l[^\r]*?\x1b\[K|\r\x1b\[K\x1b\[\?25h`)

// Without a UI that keeps what is hidden, or without a terminal, the
// output is what it is with hide_work off, byte for byte but for the
// spinner's frames, which time decides.
func TestHideWorkOff(t *testing.T) {
	run := func(cols int, hide, hider bool) (string, *fakeUI) {
		t.Helper()
		prov := &fakeProvider{replies: []*llm.Response{
			{Text: "\nlooking", ToolCalls: []llm.ToolCall{
				toolCall("c1", "read_file", `{"path":"a.txt"}`),
				toolCall("c2", "bash", `{"command":"ls"}`),
			}},
			{Text: "done"},
		}}
		a, _, sh, h, cwd := hidingAgent(t, prov)
		h.cols, a.Cfg.HideWork = cols, hide
		if !hider {
			a.UI = h.fakeUI
		}
		if err := a.Start(context.Background(), "go", tools.Exec{Dir: cwd}); err != nil {
			t.Fatal(err)
		}
		sh.outputs["c2"] = rpc.Output{Output: "a.txt\n", Cwd: cwd}
		if err := a.Resume(context.Background(), "c2", 0, tools.Exec{Dir: cwd}); err != nil {
			t.Fatal(err)
		}
		if len(h.hidden) != 0 || h.hideCmds != 0 {
			t.Errorf("%d columns, hide_work %v, hider %v: kept %q", cols, hide, hider, h.hidden)
		}
		return spinnerDraws.ReplaceAllString(h.String(), ""), h.fakeUI
	}
	for _, cols := range []int{0, 80} {
		want, wui := run(cols, false, true)
		for _, hider := range []bool{false, cols == 0} {
			got, gui := run(cols, true, hider)
			if got != want || !slices.Equal(gui.folds, wui.folds) || !slices.Equal(gui.at, wui.at) {
				t.Errorf("%d columns, hider %v:\n%q\nwant\n%q", cols, hider, got, want)
			}
		}
	}
}

// Ctrl+C ends the group: its line is the last the agent prints, before the
// shell says the request was interrupted.
func TestHideWorkInterrupted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prov := &fakeProvider{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{toolCall("c1", "read_file", `{"path":"a.txt"}`)}},
	}}
	prov.before = func(_ context.Context, n int, _ func(string)) {
		if n == 1 {
			cancel()
		}
	}
	a, _, _, ui, cwd := hidingAgent(t, prov)
	if err := a.Start(ctx, "read", tools.Exec{Dir: cwd}); err == nil {
		t.Fatal("no error from an interrupted request")
	}
	out := ui.String()
	if want := []string{"● Read 1 file" + workHint, ""}; !slices.Equal(screenOf(out), want) || !strings.HasSuffix(out, workHint+reset+"\x1b[K\n") {
		t.Errorf("screen %q, want %q; output %q", screenOf(out), want, out)
	}
	if a.work != nil || a.UI != ui {
		t.Errorf("the request is over, the UI still wrapped")
	}
}

// Blank text between calls, which models send, does not end the group.
func TestHideWorkBlankText(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{
		{Text: "\n\n", ToolCalls: []llm.ToolCall{toolCall("c1", "read_file", `{"path":"a.txt"}`)}},
		{Text: " \n", ToolCalls: []llm.ToolCall{toolCall("c2", "read_file", `{"path":"b.txt"}`)}},
		{Text: "\n\n  all read\n"},
	}}
	a, _, _, ui, cwd := hidingAgent(t, prov)
	if err := a.Start(context.Background(), "read", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"● Read 2 files" + workHint, "  all read", ""}; !slices.Equal(screenOf(ui.String()), want) {
		t.Errorf("screen %q, want %q", screenOf(ui.String()), want)
	}

	var l leadBlanks
	if l.text("\n x") != "\n x" {
		t.Error("blanks left out while off")
	}
	l = leadBlanks{on: true}
	for _, tc := range []struct{ in, want string }{{"\n", ""}, {" \r\n  ", ""}, {"x\n", "  x\n"}, {"\n", "\n"}} {
		if got := l.text(tc.in); got != tc.want {
			t.Errorf("%q: %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A request after one whose command the shell cut short forgets that
// group: the proxy ended its line, and it is not drawn again.
func TestHideWorkFreshRequest(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{toolCall("c1", "bash", `{"command":"sleep 9"}`)}},
		{Text: "hi"},
	}}
	a, _, _, ui, cwd := hidingAgent(t, prov)
	if err := a.Start(context.Background(), "wait", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if a.work == nil || a.UI == ui {
		t.Fatal("no group waiting for the command")
	}
	ui.Reset()
	if err := a.Start(context.Background(), "hello", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if out := ui.String(); strings.Contains(out, "●") || !strings.Contains(out, "hi") {
		t.Errorf("second request printed %q", out)
	}
}

// The task tool finds the panes through the workUI.
func TestWorkUIPanes(t *testing.T) {
	g := &workGroup{}
	if _, ok := wrapWork(&fakeUI{}, g).(Panes); ok {
		t.Error("panes made up")
	}
	if _, ok := wrapWork(&panesUI{fakeUI: &fakeUI{}}, g).(Panes); !ok {
		t.Error("panes lost")
	}
}

// slowTool takes its time, as task_wait or a server's tool may.
type slowTool struct {
	namedTool
	d time.Duration
}

func (t slowTool) Execute(ctx context.Context, _ tools.Exec, _ map[string]any, _ io.Writer) (string, error) {
	select {
	case <-time.After(t.d):
		return "waited", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// groupFrames are the line of a group calling one tool as it turns, the
// frame captured.
var groupFrames = regexp.MustCompile(`\r\x1b\[36m(.) \x1b\[2mCalling 1 tool…\x1b\[0m\x1b\[K`)

// A long call turns the line of the group while it runs, and stops before
// its result is kept: nothing turns it after.
func TestHideWorkSlowCall(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{toolCall("c1", "wait", `{}`)}},
		{Text: "done"},
	}}
	a, _, _, ui, cwd := hidingAgent(t, prov)
	a.Tools.Add(slowTool{namedTool{name: "wait"}, 500 * time.Millisecond})
	if err := a.Start(context.Background(), "wait", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if len(ui.keptAt) != 1 {
		t.Fatalf("kept %q", ui.hidden)
	}
	out := ui.String()
	frames := map[string]bool{}
	for _, m := range groupFrames.FindAllStringSubmatch(out[:ui.keptAt[0]], -1) {
		frames[m[1]] = true
	}
	if len(frames) < 3 {
		t.Errorf("the line turned through %d frames while the call ran: %q", len(frames), out[:ui.keptAt[0]])
	}
	// After it the line is the turn's spinner's, which hides the cursor,
	// and then done.
	if rest := out[ui.keptAt[0]:]; groupFrames.MatchString(rest) {
		t.Errorf("the line turned after the result: %q", rest)
	}
	if want := []string{"● Called 1 tool" + workHint, "done", ""}; !slices.Equal(screenOf(out), want) {
		t.Errorf("screen %q, want %q", screenOf(out), want)
	}
}

// The line handed to the UI with a command goes on from the frame drawn,
// with the label drawn, within the columns it is given.
func TestHideWorkTurner(t *testing.T) {
	prov := &fakeProvider{replies: []*llm.Response{
		{ToolCalls: []llm.ToolCall{
			toolCall("c1", "read_file", `{"path":"a.txt"}`),
			toolCall("c2", "bash", `{"command":"sleep 5"}`),
		}},
	}}
	a, _, _, ui, cwd := hidingAgent(t, prov)
	if err := a.Start(context.Background(), "wait", tools.Exec{Dir: cwd}); err != nil {
		t.Fatal(err)
	}
	if len(ui.lines) != 1 {
		t.Fatalf("%d lines to turn", len(ui.lines))
	}
	line := ui.lines[0]
	drawn := screenOf(ui.String())
	if got := stripCSI(line(0, 80)); len(drawn) != 1 || got != drawn[0] {
		t.Errorf("frame 0 %q, drawn %q", got, drawn)
	}
	first, _ := utf8.DecodeRuneInString(drawn[0])
	at := slices.Index(spinnerFrames, string(first))
	for n := 1; n <= len(spinnerFrames); n++ {
		got := stripCSI(line(n, 80))
		want := spinnerFrames[(at+n)%len(spinnerFrames)] + " Reading 1 file, running 1 command…"
		if got != want {
			t.Errorf("frame %d %q, want %q", n, got, want)
		}
	}
	for _, cols := range []int{10, 20, 40} {
		if got := stripCSI(line(1, cols)); runewidth.StringWidth(got) > cols-1 || !strings.HasSuffix(got, "…") {
			t.Errorf("%d columns: %q", cols, got)
		}
	}
}
