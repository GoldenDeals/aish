// Package agent works on the user's requests: it talks to the LLM, executes
// tools after checking the policy, and hands bash commands to the live
// shell (see init.bash). One Agent lives in the proxy for the whole shell;
// the thin `aish agent start` and `aish agent resume` commands inside the
// shell tell it over RPC to begin a request or to go on after a command,
// and each call works until the next bash command or the final answer. The
// shell runs that command and resumes the agent with its exit status.
package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/mattn/go-runewidth"

	"github.com/inebotov/aish/internal/capture"
	"github.com/inebotov/aish/internal/config"
	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/policy"
	"github.com/inebotov/aish/internal/rpc"
	"github.com/inebotov/aish/internal/session"
	"github.com/inebotov/aish/internal/subagent"
	"github.com/inebotov/aish/internal/tools"
)

const (
	dim   = "\x1b[2m"
	bold  = "\x1b[1m"
	red   = "\x1b[31m"
	cyan  = "\x1b[36m"
	reset = "\x1b[0m"
)

// Journal is the session's journal as the proxy keeps it. ID and Len tell
// the agent whether what it read is still the journal: `clear` and `aish
// resume` replace it.
type Journal interface {
	ID() string
	Len() int
	Entries() []session.Entry
	Append(...session.Entry) error
}

// Shell runs bash commands for the agent: the user's live shell, reached
// through the proxy.
type Shell interface {
	// HandOff leaves cmd for the shell to run as tool call id.
	HandOff(id, cmd string) error
	// Wait returns what the shell ran for call id, waiting up to timeout
	// for it to finish.
	Wait(ctx context.Context, id string, timeout time.Duration) (rpc.Output, error)
}

// UI is the terminal the agent shows its work on.
type UI interface {
	io.Writer
	// Size is the terminal's; cols 0 means there is none: plain text, no
	// spinner.
	Size() (cols, rows int)
	// Ask prints q and returns the line the user answers with. An error
	// means nobody can answer.
	Ask(ctx context.Context, q string) (string, error)
	// Form asks the user qs one at a time and returns an answer to each;
	// nil answers mean the user cancelled. An error means nobody can
	// answer, or ctx ended: then the form is gone from the screen.
	Form(ctx context.Context, qs []Question) ([]Answer, error)
	// Fold keeps text, a tool's result, behind "ctrl+o to expand" and
	// shows its status, which ends the line.
	Fold(title, text string)
	// Live is where an external tool's output goes as it runs: shown
	// folded, like a command's.
	Live(title string) Live
	// CommandAt says the call, a bash command or another tool's, was
	// printed without a newline and ends at column col, so the status can
	// go at the right edge of that line: the status of the next Fold, Live
	// or command the shell runs. long when the call took several lines.
	// hidden is how many lines of the command were not printed: the UI
	// keeps the command for Ctrl+O even without output.
	CommandAt(col int, long bool, hidden int)
}

// Live is a tool's output being shown; Finish ends it with the tool's exit
// status, -1 when unknown.
type Live interface {
	io.Writer
	Finish(exit int)
}

// Agent keeps what one request needs between the calls that drive it: the
// journal as read, the tools the model was given, the shell's situation.
// Cfg, Provider, Tools and Policy are set by whoever hosts the agent, before
// each call.
type Agent struct {
	Cfg      config.Config
	Provider llm.Provider
	Tools    *tools.Registry
	Policy   *policy.Engine
	Journal  Journal
	Shell    Shell
	UI       UI

	entries []session.Entry
	sess    string // Journal.ID() the entries were read from
	seen    int    // Journal.Len() after the agent last read or wrote it
	env     string // see environment; built once per request
	mask    *Masker
	maskKey string // the config the mask was built from: it is reloaded per request
	exec    tools.Exec
	hooks   hookState // found once per request
	// subs are the subagents the task tool runs: see AddSubagents.
	subs []subagent.Def
	// bg are the subagents in the background, made on the first one and
	// kept for the agent's life: they outlive the request that started
	// them. Under bgMu: the proxy stops them between requests.
	bgMu sync.Mutex
	bg   *bgSet
	// name is the subagent this agent is, "" for the host agent. Its
	// calls carry it to the pre-tool hooks; the policy has it from the
	// engine prepSub gives the subagent as well (Engine.Subagent).
	name string
	// windowFull is set when the context window cut the last reply: the
	// session is summed up before the next turn, however small the estimate.
	// Like the agent it lives through to the next request.
	windowFull bool
	// compactFailed is set when a summary past compact_at could not be
	// made: autoCompact does not ask for it again till the next request.
	compactFailed bool
	// paused is set when the API broke the last turn off for the model to
	// go on from it: drive makes another turn, though the reply looks final.
	paused bool
}

// Start records a new request made in ex and works on it.
func (a *Agent) Start(ctx context.Context, text string, ex tools.Exec) error {
	a.exec, a.env, a.compactFailed = ex, "", false
	a.load(true)
	if err := a.closePending(ctx); err != nil {
		return err
	}
	cwd := ex.Dir
	a.loadHooks()
	added, ok, err := a.userPrompt(ctx, text, cwd)
	if err != nil || !ok {
		return err
	}
	inst := instructions(a.entries, cwd)
	for _, e := range inst {
		fmt.Fprintf(a.UI, "%s  (%s)%s\n", dim, tildePath(e.Path), reset)
	}
	if err := a.append(inst...); err != nil {
		return err
	}
	files := mentions(text, cwd)
	for _, e := range files {
		fmt.Fprintf(a.UI, "%s  %s%s\n", dim, mentionNote(e, cwd), reset)
	}
	if err := a.append(files...); err != nil {
		return err
	}
	used := skillMentions(text, cwd)
	for _, e := range used {
		fmt.Fprintf(a.UI, "%s  %s%s\n", dim, skillNote(e, cwd), reset)
	}
	if err := a.append(used...); err != nil {
		return err
	}
	if err := a.append(added...); err != nil {
		return err
	}
	if err := a.append(session.Entry{Kind: session.KindUser, Text: text, Cwd: cwd}); err != nil {
		return err
	}
	return a.drive(ctx)
}

// Resume records the result of the bash command with the given call id and
// continues; ex is the shell after the command.
func (a *Agent) Resume(ctx context.Context, id string, rc int, ex tools.Exec) error {
	a.exec = ex
	a.load(false)
	var call *session.ToolCall
	for _, c := range pending(a.entries) {
		if c.ID == id {
			call = &c
			break
		}
	}
	if call == nil {
		return fmt.Errorf("no pending tool call %s", id)
	}
	out, err := a.Shell.Wait(ctx, id, 10*time.Second)
	if err != nil {
		out = rpc.Output{Output: "(output was not captured: " + err.Error() + ")", Exit: rc}
	}
	if err := a.postTool(ctx, *call, a.hooks.handed(id), bashResult(out, a.Cfg.MaxOutputBytes), false); err != nil {
		return err
	}
	return a.drive(ctx)
}

// closePending gives a result to the tool calls a request interrupted with
// Ctrl+C left without one: every call needs one.
func (a *Agent) closePending(ctx context.Context) error {
	for _, c := range pending(a.entries) {
		msg := "interrupted by the user"
		if t, ok := a.Tools.Get(c.Name); ok && handsOff(t) {
			if out, err := a.Shell.Wait(ctx, c.ID, 0); err == nil && out.Output != "" {
				msg = capture.Truncate(out.Output, a.Cfg.MaxOutputBytes) + "\n[" + msg + "]"
			}
		}
		if err := a.append(toolResult(c, msg, true)); err != nil {
			return err
		}
	}
	return nil
}

// load reads the journal from the last summary on: nothing before it is
// sent, and instruction files read before it are read again. Unless full,
// the entries are kept when the journal is the one they came from: within
// a request nothing but the agent writes it, and a journal of any size
// costs the same.
func (a *Agent) load(full bool) {
	if id := a.Journal.ID(); !full && a.entries != nil && id == a.sess && a.Journal.Len() == a.seen {
		return
	}
	es := a.Journal.Entries()
	a.entries = session.Current(es)
	if a.entries == nil {
		a.entries = []session.Entry{}
	}
	a.sess, a.seen = a.Journal.ID(), len(es)
}

func (a *Agent) append(es ...session.Entry) error {
	for i := range es {
		if es[i].Time.IsZero() {
			es[i].Time = time.Now()
		}
	}
	a.entries = append(a.entries, es...)
	err := a.Journal.Append(es...)
	a.seen += len(es)
	return err
}

// maxPauses is how many turns in a row the API may pause before the
// request ends as if the model were done.
const maxPauses = 3

// drive runs tool calls and LLM turns until a bash command is handed to the
// shell or the model gives its final answer.
func (a *Agent) drive(ctx context.Context) (err error) {
	handedOff := false
	defer func() {
		if err != nil || !handedOff {
			a.noteBackground() // the request ends here
		}
	}()
	pauses := 0
	compacted := false // since the last turn the API took
	for {
		for _, c := range pending(a.entries) {
			handedOff, err = a.call(ctx, c)
			if err != nil || handedOff {
				return err
			}
		}
		if a.paused && pauses < maxPauses {
			a.paused = false
			pauses++
		} else {
			if a.paused {
				fmt.Fprintf(a.UI, "%s[aish: the model paused its turn; ask to continue]%s\n", dim, reset)
			}
			a.paused, pauses = false, 0
			if finished(a.entries) {
				a.stop(ctx)
				return nil
			}
		}
		if a.Cfg.MaxSteps > 0 && steps(a.entries) >= a.Cfg.MaxSteps {
			fmt.Fprintf(a.UI, "%s[aish: stopped after %d steps; ask to continue]%s\n", dim, a.Cfg.MaxSteps, reset)
			if err := a.append(session.Entry{Kind: session.KindAssistant, Text: fmt.Sprintf("(stopped after %d steps)", a.Cfg.MaxSteps)}); err != nil {
				return err
			}
			a.stop(ctx)
			return nil
		}
		if err := a.autoCompact(ctx); err != nil {
			return err
		}
		err := a.turn(ctx)
		switch {
		case err == nil:
			compacted = false
		case ctx.Err() != nil || !llm.PromptTooLong(err):
			return err
		case a.Cfg.CompactAt <= 0:
			// Not wrapped: the proxy would print the SDK's error alone.
			return fmt.Errorf("%s; aish compact frees it", llm.Short(err))
		case compacted || a.windowFull || !compactable(a.entries):
			// The summary did not free the window, could not be made (it
			// was tried before this turn), or there is nothing to sum up:
			// the request itself is too big.
			return err
		default:
			// The estimate said the context fits; the API counts for sure.
			// autoCompact says it compacts, as for a reply the window cut.
			a.windowFull, compacted = true, true
		}
	}
}

// turn asks the model for the next assistant message and records it.
func (a *Agent) turn(ctx context.Context) error {
	req := a.request(a.entries)
	var streamed strings.Builder
	cols, _ := a.UI.Size()
	sp := startSpinner(a.UI, cols > 0)
	md := newMarkdown(sp, a.UI.Size, a.Cfg)
	resp, err := a.complete(ctx, req, func(s string) {
		io.WriteString(md, s)
		streamed.WriteString(s)
	}, func(note string) {
		md.Flush()
		sp.Stop()
		fmt.Fprintf(a.UI, "%s%s%s\n", dim, note, reset)
		streamed.Reset()
		sp = startSpinner(a.UI, cols > 0)
		md = newMarkdown(sp, a.UI.Size, a.Cfg)
	})
	md.Flush()
	sp.Stop()
	if err != nil {
		if streamed.Len() > 0 {
			// Keep what the user saw, so the model knows it was cut off.
			why := "interrupted by the user"
			if ctx.Err() == nil {
				why = "cut off by an API error: " + llm.Short(err)
			}
			_ = a.append(session.Entry{Kind: session.KindAssistant, Text: streamed.String() + "\n[" + why + "]"})
		}
		return err
	}
	e := session.Entry{
		Kind: session.KindAssistant, Text: resp.Text, Raw: resp.Raw,
		Provider: a.Provider.Name(), Model: a.Provider.Model(), Profile: a.Cfg.Profile,
		InputTokens: resp.InputTokens, CachedTokens: resp.CachedTokens, OutputTokens: resp.OutputTokens,
	}
	for _, c := range resp.ToolCalls {
		e.ToolCalls = append(e.ToolCalls, session.ToolCall{ID: c.ID, Name: c.Name, Args: c.Args})
	}
	switch resp.StopReason {
	case llm.StopMaxTokens:
		fmt.Fprintf(a.UI, "%s[aish: reply cut at max_tokens]%s\n", dim, reset)
	case llm.StopContextWindow:
		a.windowFull = true
		hint := ""
		if a.Cfg.CompactAt <= 0 {
			hint = "; aish compact frees it"
		}
		fmt.Fprintf(a.UI, "%s[aish: reply cut: the context window is full%s]%s\n", dim, hint, reset)
	case llm.StopRefusal:
		fmt.Fprintf(a.UI, "%s[aish: the model declined to answer]%s\n", dim, reset)
	case llm.StopPause:
		// Sent back as is (Raw), the reply is where the model goes on from.
		a.paused = true
	}
	return a.append(e)
}

func (a *Agent) request(entries []session.Entry) llm.Request {
	extra := a.Cfg.SystemPrompt
	if note := a.policyPrompt(); note != "" {
		extra = strings.TrimSpace(note + "\n\n" + extra)
	}
	if note := a.toolsPrompt(); note != "" {
		extra = strings.TrimSpace(note + "\n\n" + extra)
	}
	if a.env == "" {
		// From a.entries, not entries: Compact adds its prompt as a request
		// and must send the system prompt the previous turns were cached with.
		a.env = environment(requestCwd(a.entries, a.exec.Dir))
	}
	if key := fmt.Sprint(a.Cfg.MaskDefaults, a.Cfg.Mask); a.mask == nil || key != a.maskKey {
		m, err := NewMasker(a.Cfg.MaskDefaults, a.Cfg.Mask)
		if err != nil { // config.Load rejects these; keep the built-in ones
			fmt.Fprintf(a.UI, "%s[aish: %v]%s\n", dim, err, reset)
			m, _ = NewMasker(true, nil)
		}
		a.mask, a.maskKey = m, key
	}
	return llm.Request{
		System:   system(a.env, extra),
		Messages: Messages(ownRaw(entries, a.Cfg.Profile), a.Cfg.MaxOutputBytes, a.mask),
		Tools:    a.toolDefs(entries),
	}
}

// call executes one tool call. For a tool that hands its command off
// (bash) it leaves the command for the shell and reports handedOff; the
// result arrives with Resume. A dialog (ask_user) the user answers.
func (a *Agent) call(ctx context.Context, c session.ToolCall) (handedOff bool, err error) {
	t, ok := a.tool(c.Name)
	if !ok {
		return false, a.append(toolResult(c, "unknown tool "+c.Name, true))
	}
	args, err := tools.Decode(c.Args)
	if err != nil {
		return false, a.append(toolResult(c, err.Error(), true))
	}

	in := policy.NewInput(t.Name(), args, a.exec.Dir, a.exec.Env)
	if h, ok := t.(tools.HandsOff); ok {
		if line, ok := h.Command(args); ok {
			in.HandOff(line)
		}
	}
	in.Server = tools.ServerOf(t)
	in.Model = a.Cfg.Model
	in.Agent = a.name
	d, err := a.Policy.Check(ctx, in)
	if err != nil {
		return false, err
	}
	v, err := a.preTool(ctx, t, c, in, d)
	if err != nil {
		return false, err
	}
	d, args = v.Decision, v.args
	h, toShell := t.(tools.HandsOff)
	var cmd string
	var hasCmd bool
	if toShell {
		cmd, hasCmd = h.Command(args)
	}
	// The call is shown once the policy and the hooks have decided, with
	// what it runs: nothing they print can come between its line and the
	// status at the right of it.
	title := tools.Title(t, args)
	showClosed := func() {
		if toShell {
			a.showBash(cmd, true)
		} else {
			a.show(title, true)
		}
	}
	asked := d.Action == policy.Ask
	if asked {
		showClosed()
		d = a.ask(ctx, d)
		if ctx.Err() != nil {
			return false, ctx.Err() // the call stays pending: interrupted, not declined
		}
	}
	if d.Action == policy.Deny {
		msg := "denied by " + v.by
		if d.Reason != "" {
			msg += ": " + d.Reason
		}
		if !asked {
			showClosed()
		}
		fmt.Fprintf(a.UI, "%s  ✗ %s%s\n", red, msg, reset)
		return false, a.append(toolResult(c, msg, true))
	}
	// Ctrl+C may have come after the last hook was done and the policy had
	// answered: an interrupted request hands nothing to the shell and runs
	// nothing. The call stays pending, as with the question above.
	if err := ctx.Err(); err != nil {
		return false, err
	}

	if tools.IsDialog(t) {
		// The form opens below the call, and its answers are no status:
		// the line is closed.
		if !asked {
			showClosed()
		}
		return false, a.dialog(ctx, c, args)
	}
	if toShell {
		if !hasCmd {
			return false, a.append(toolResult(c, "empty command", true))
		}
		if !asked {
			a.showBash(cmd, false)
		}
		return true, a.Shell.HandOff(c.ID, cmd)
	}

	col := -1 // where the line of the call was left open, if it was
	if !asked {
		col = a.show(title, false)
	}
	title = "⚙ " + title
	// Streaming tools (external ones) print live, folded like a command's
	// output; the others print nothing until they are done.
	streams := tools.Streams(t)
	var out io.Writer
	var live Live
	if streams {
		if col >= 0 {
			a.UI.CommandAt(col, false, 0)
		}
		live = a.UI.Live(title)
		out = live
		col = -1 // the UI ends the line
	}
	res, err := t.Execute(ctx, a.exec, args, out)
	if live != nil {
		exit := -1
		if errors.Is(ctx.Err(), context.Canceled) {
			exit = 130
		}
		live.Finish(exit)
	}
	if err != nil {
		if col >= 0 {
			fmt.Fprint(a.UI, "\n")
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return false, ctx.Err()
		}
		fmt.Fprintf(a.UI, "%s  ✗ %v%s\n", red, err, reset)
		return false, a.postTool(ctx, c, args, strings.TrimSpace(res+"\n"+err.Error()), true)
	}
	if !streams {
		// A longer result is kept for Ctrl+O, and the UI shows its status;
		// a one-line result is no status: it is shown as is, below the call.
		if strings.Contains(strings.TrimSpace(res), "\n") {
			if col >= 0 {
				a.UI.CommandAt(col, false, 0)
			}
			a.UI.Fold(title, res)
		} else {
			if col >= 0 {
				fmt.Fprint(a.UI, "\n")
			}
			fmt.Fprintf(a.UI, "%s  %s%s\n", dim, summary(res), reset)
		}
	}
	return false, a.postTool(ctx, c, args, capture.Truncate(res, a.Cfg.MaxOutputBytes*4), false)
}

// show prints the call of a tool other than bash, titled title. Unless nl,
// the line is left open when the output will be folded from its first
// line, and show returns the column it ends at, for the status to go at
// the right edge of it; -1 when the line is closed.
func (a *Agent) show(title string, nl bool) int {
	cols, _ := a.UI.Size()
	open := !nl && a.Cfg.FoldLines == 0 && cols > 0
	text, col := renderCall(title, cols, open)
	fmt.Fprint(a.UI, text)
	return col
}

// shortStatus is the widest short status the proxy draws at the right
// edge, as long as the counts go.
const shortStatus = "  (99999 lines · exit 255)"

// callMin is the narrowest a call line is cut to for the status to fit
// beside it; in a narrower terminal it takes the whole width and the
// status goes below.
const callMin = 20

// renderCall is what show prints for a call titled title and, when the
// line is left open for the status, the column it ends at; -1 when it is
// closed. An open line is a single one, cut with "…" to leave room for the
// short status and the column the proxy keeps free at the right edge. The
// whole title is in Ctrl+O and in the journal.
func renderCall(title string, cols int, open bool) (text string, col int) {
	if !open || cols <= 0 {
		return fmt.Sprintf("%s⚙%s %s\n", cyan, reset, title), -1
	}
	// Control characters would take columns of their own, or none.
	title = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, title)
	w := cols - runewidth.StringWidth(shortStatus) - 1
	if w < callMin {
		w = cols
	}
	title = runewidth.Truncate(title, w-runewidth.StringWidth("⚙ "), "…")
	return fmt.Sprintf("%s⚙%s %s", cyan, reset, title), runewidth.StringWidth("⚙ " + title)
}

// handsOff tells whether a call of t is a command for the shell.
func handsOff(t tools.Tool) bool {
	_, ok := t.(tools.HandsOff)
	return ok
}

// showBash prints a command for the shell: exactly what will run, which the
// user may be asked about. Unless nl, the line is left open when the
// output will be folded from its first line, and the UI is told where the
// command ends so the status can go to its right.
func (a *Agent) showBash(cmd string, nl bool) {
	cols, _ := a.UI.Size()
	open := !nl && a.Cfg.FoldLines == 0 && cols > 0
	text, col, long, hidden := renderBash(cmd, cols, open)
	fmt.Fprint(a.UI, text)
	if col >= 0 {
		a.UI.CommandAt(col, long, hidden)
	}
}

// cmdLines is how many lines of a bash command are shown; the rest is
// summed up in one line and kept for Ctrl+O by the proxy.
const cmdLines = 3

// renderBash is what showBash prints for cmd and, when the line is left
// open for the status, where it ends: col, long and how many lines are hidden.
// A closed line (col -1) shows the whole command: the user may be asked
// about it, and without CommandAt the proxy would not keep the rest for
// Ctrl+O.
func renderBash(cmd string, cols int, open bool) (text string, col int, long bool, hidden int) {
	lines := strings.Split(strings.TrimRight(cmd, "\n"), "\n")
	if !open || cols <= 0 {
		return fmt.Sprintf("%s❯%s %s%s%s\n", cyan, reset, bold, strings.Join(lines, "\n  "), reset), -1, false, 0
	}
	shown := lines
	if len(lines) > cmdLines {
		shown, hidden = lines[:cmdLines], len(lines)-cmdLines
	}
	text = fmt.Sprintf("%s❯%s %s%s%s", cyan, reset, bold, strings.Join(shown, "\n  "), reset)
	last := "  " + shown[len(shown)-1]
	if len(shown) == 1 {
		last = "❯ " + shown[0]
	}
	if hidden > 0 {
		last = fmt.Sprintf("  … (+%d lines)", hidden)
		if hidden == 1 {
			last = "  … (+1 line)"
		}
		text += "\n" + dim + last + reset
	}
	w := runewidth.StringWidth(last)
	col = w % cols
	if w > 0 && col == 0 {
		col = cols // the cursor waits at the right edge
	}
	return text, col, len(lines) > 1 || w > cols, hidden
}

// ask lets the user decide an "ask" verdict on the terminal.
func (a *Agent) ask(ctx context.Context, d policy.Decision) policy.Decision {
	q := "allow?"
	if d.Reason != "" {
		q = d.Reason + " — allow?"
	}
	ans, err := a.UI.Ask(ctx, fmt.Sprintf("%s%s%s", bold, q, reset))
	if err != nil {
		return policy.Decision{Action: policy.Deny, Reason: "needs confirmation, no terminal: " + d.Reason}
	}
	switch strings.ToLower(strings.TrimSpace(ans)) {
	case "y", "yes", "д", "да":
		return policy.Decision{Action: policy.Allow}
	}
	return policy.Decision{Action: policy.Deny, Reason: "the user declined"}
}

func toolResult(c session.ToolCall, out string, isErr bool) session.Entry {
	return session.Entry{Kind: session.KindToolResult, ToolCallID: c.ID, ToolName: c.Name, Output: out, IsError: isErr}
}

func bashResult(o rpc.Output, max int) string {
	var b strings.Builder
	if o.TUI {
		b.WriteString(o.Output)
	} else {
		b.WriteString(capture.Truncate(o.Output, max))
	}
	if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n") {
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "[exit %d, cwd %s]", o.Exit, o.Cwd)
	return b.String()
}

func summary(s string) string {
	s = strings.TrimSpace(s)
	if n := strings.Count(s, "\n"); n > 0 {
		return fmt.Sprintf("%d lines", n+1)
	}
	if len(s) > 120 {
		return s[:120] + "…"
	}
	return s
}

func tildePath(p string) string {
	if home, err := os.UserHomeDir(); err == nil {
		if rest, ok := strings.CutPrefix(p, home+"/"); ok {
			return "~/" + rest
		}
	}
	return p
}
