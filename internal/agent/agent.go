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
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/inebotov/aish/internal/capture"
	"github.com/inebotov/aish/internal/config"
	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/policy"
	"github.com/inebotov/aish/internal/rpc"
	"github.com/inebotov/aish/internal/session"
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
	// Fold keeps text, a tool's result, behind "ctrl+o to expand".
	Fold(title, text string)
	// Live is where an external tool's output goes as it runs: shown
	// folded, like a command's.
	Live(title string) Live
	// CommandAt says the bash command was printed without a newline and
	// ends at column col, so the status can go to its right; long when it
	// took several lines.
	CommandAt(col int, long bool)
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
}

// Start records a new request made in ex and works on it.
func (a *Agent) Start(ctx context.Context, text string, ex tools.Exec) error {
	a.exec, a.env = ex, ""
	a.load(true)
	if err := a.closePending(ctx); err != nil {
		return err
	}
	cwd := ex.Dir
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
	if err := a.append(toolResult(*call, bashResult(out, a.Cfg.MaxOutputBytes), false)); err != nil {
		return err
	}
	return a.drive(ctx)
}

// closePending gives a result to the tool calls a request interrupted with
// Ctrl+C left without one: every call needs one.
func (a *Agent) closePending(ctx context.Context) error {
	for _, c := range pending(a.entries) {
		msg := "interrupted by the user"
		if c.Name == tools.Bash {
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

// mcpNote tells the model about MCP tools it is not given schemas for.
const mcpNote = "# Additional tools\n" +
	"More tools (from MCP servers) are available as shell commands, run with the bash tool: " +
	"`aish tool` lists them, `aish tool NAME -h` shows how to call one. They are named SERVER_TOOL."

// drive runs tool calls and LLM turns until a bash command is handed to the
// shell or the model gives its final answer.
func (a *Agent) drive(ctx context.Context) error {
	for {
		for _, c := range pending(a.entries) {
			handedOff, err := a.call(ctx, c)
			if err != nil || handedOff {
				return err
			}
		}
		if finished(a.entries) {
			return nil
		}
		if a.Cfg.MaxSteps > 0 && steps(a.entries) >= a.Cfg.MaxSteps {
			fmt.Fprintf(a.UI, "%s[aish: stopped after %d steps; ask to continue]%s\n", dim, a.Cfg.MaxSteps, reset)
			return a.append(session.Entry{Kind: session.KindAssistant, Text: fmt.Sprintf("(stopped after %d steps)", a.Cfg.MaxSteps)})
		}
		if err := a.autoCompact(ctx); err != nil {
			return err
		}
		if err := a.turn(ctx); err != nil {
			return err
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
	resp, err := a.Provider.Complete(ctx, req, func(s string) {
		io.WriteString(md, s)
		streamed.WriteString(s)
	})
	md.Flush()
	sp.Stop()
	if err != nil {
		if ctx.Err() != nil && streamed.Len() > 0 {
			// Keep what the user saw, so the model knows it was cut off.
			_ = a.append(session.Entry{Kind: session.KindAssistant, Text: streamed.String() + "\n[interrupted by the user]"})
		}
		return err
	}
	e := session.Entry{
		Kind: session.KindAssistant, Text: resp.Text, Raw: resp.Raw,
		Provider: a.Provider.Name(), Model: a.Provider.Model(),
		InputTokens: resp.InputTokens, CachedTokens: resp.CachedTokens, OutputTokens: resp.OutputTokens,
	}
	for _, c := range resp.ToolCalls {
		e.ToolCalls = append(e.ToolCalls, session.ToolCall{ID: c.ID, Name: c.Name, Args: c.Args})
	}
	if resp.StopReason == "max_tokens" || resp.StopReason == "length" {
		fmt.Fprintf(a.UI, "%s[aish: reply cut at max_tokens]%s\n", dim, reset)
	}
	return a.append(e)
}

func (a *Agent) request(entries []session.Entry) llm.Request {
	extra := a.Cfg.SystemPrompt
	hidden := false
	for _, t := range a.Tools.All() {
		hidden = hidden || t.Hidden
	}
	if hidden {
		extra = strings.TrimSpace(mcpNote + "\n\n" + extra)
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
	req := llm.Request{
		System:   system(a.env, extra),
		Messages: Messages(entries, a.Cfg.MaxOutputBytes, a.mask),
	}
	for _, t := range a.Tools.All() {
		if t.Hidden {
			continue
		}
		req.Tools = append(req.Tools, llm.ToolDef{Name: t.Name, Description: t.Desc, Schema: t.Schema()})
	}
	return req
}

// call executes one tool call. For bash it leaves the command for the shell
// and reports handedOff; the result arrives with Resume.
func (a *Agent) call(ctx context.Context, c session.ToolCall) (handedOff bool, err error) {
	t, ok := a.Tools.Get(c.Name)
	if !ok {
		return false, a.append(toolResult(c, "unknown tool "+c.Name, true))
	}
	args, err := tools.Decode(c.Args)
	if err != nil {
		return false, a.append(toolResult(c, err.Error(), true))
	}
	title := a.show(t, args)

	in := policy.NewInput(t.Name, args, a.exec.Dir)
	in.Server = t.Server
	in.Model = a.Cfg.Model
	d, err := a.Policy.Check(ctx, in)
	if err != nil {
		return false, err
	}
	asked := d.Action == policy.Ask
	if asked {
		if t.Name == tools.Bash {
			a.showBash(args, true)
		}
		d = a.ask(ctx, d)
		if ctx.Err() != nil {
			return false, ctx.Err() // the call stays pending: interrupted, not declined
		}
	}
	if d.Action == policy.Deny {
		msg := "denied by policy"
		if d.Reason != "" {
			msg += ": " + d.Reason
		}
		if t.Name == tools.Bash && !asked {
			a.showBash(args, true)
		}
		fmt.Fprintf(a.UI, "%s  ✗ %s%s\n", red, msg, reset)
		return false, a.append(toolResult(c, msg, true))
	}

	if t.Name == tools.Bash {
		cmd, _ := args["command"].(string)
		if strings.TrimSpace(cmd) == "" {
			return false, a.append(toolResult(c, "empty command", true))
		}
		if !asked {
			a.showBash(args, false)
		}
		return true, a.Shell.HandOff(c.ID, cmd)
	}

	// External tools print live, folded like a command's output; built-ins
	// print nothing until they are done.
	var out io.Writer
	var live Live
	if t.Path != "" {
		live = a.UI.Live(title)
		out = live
	}
	res, err := t.ExecuteIn(ctx, a.exec, args, out)
	if live != nil {
		exit := -1
		if errors.Is(ctx.Err(), context.Canceled) {
			exit = 130
		}
		live.Finish(exit)
	}
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return false, ctx.Err()
		}
		fmt.Fprintf(a.UI, "%s  ✗ %v%s\n", red, err, reset)
		return false, a.append(toolResult(c, strings.TrimSpace(res+"\n"+err.Error()), true))
	}
	if t.Run != nil {
		// A one-line result is shown as is, a longer one is kept for Ctrl+O.
		line := summary(res)
		if strings.Contains(strings.TrimSpace(res), "\n") {
			a.UI.Fold(title, res)
			line = "(" + line + " · ctrl+o to expand)"
		}
		fmt.Fprintf(a.UI, "%s  %s%s\n", dim, line, reset)
	}
	return false, a.append(toolResult(c, capture.Truncate(res, a.Cfg.MaxOutputBytes*4), false))
}

// show prints the call and returns it as a plain title. A bash command is
// printed later, by showBash, once the policy has decided.
func (a *Agent) show(t tools.Tool, args map[string]any) string {
	if t.Name == tools.Bash {
		cmd, _ := args["command"].(string)
		return "❯ " + cmd
	}
	var parts []string
	for _, arg := range t.Args {
		v, ok := args[arg.Name]
		if !ok {
			continue
		}
		s := fmt.Sprint(v)
		if arg.Stdin || len(s) > 80 || strings.Contains(s, "\n") {
			s = fmt.Sprintf("<%d bytes>", len(s))
		}
		parts = append(parts, s)
	}
	fmt.Fprintf(a.UI, "%s⚙%s %s %s\n", cyan, reset, t.Name, strings.Join(parts, " "))
	return strings.TrimSpace("⚙ " + t.Name + " " + strings.Join(parts, " "))
}

// showBash prints a bash command. Unless nl, the line is left open when the
// output will be folded from its first line, and the UI is told where the
// command ends so the status can go to its right.
func (a *Agent) showBash(args map[string]any, nl bool) {
	cmd, _ := args["command"].(string)
	lines := strings.Split(cmd, "\n")
	fmt.Fprintf(a.UI, "%s❯%s %s%s%s", cyan, reset, bold, strings.Join(lines, "\n  "), reset)
	cols, _ := a.UI.Size()
	if nl || a.Cfg.FoldLines != 0 || cols <= 0 {
		fmt.Fprintln(a.UI)
		return
	}
	last := "  " + lines[len(lines)-1]
	if len(lines) == 1 {
		last = "❯ " + cmd
	}
	w := runewidth.StringWidth(last)
	col := w % cols
	if w > 0 && col == 0 {
		col = cols // the cursor waits at the right edge
	}
	a.UI.CommandAt(col, len(lines) > 1 || w > cols)
}

// ask lets the user decide an "ask" verdict on the terminal.
func (a *Agent) ask(ctx context.Context, d policy.Decision) policy.Decision {
	q := "allow?"
	if d.Reason != "" {
		q = d.Reason + " — allow?"
	}
	ans, err := a.UI.Ask(ctx, fmt.Sprintf("%s%s [y/N] %s", bold, q, reset))
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
