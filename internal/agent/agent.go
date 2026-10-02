// Package agent runs one user request: it talks to the LLM, executes tools
// after checking the policy, and hands bash commands back to the live shell
// (see init.bash): `aish agent start` and `aish agent resume` each run until
// the next bash command or the final answer. The shell runs that command and
// resumes the agent with its exit status.
package agent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"
	"golang.org/x/term"

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

type Agent struct {
	Cfg      config.Config
	Provider llm.Provider
	Tools    *tools.Registry
	Policy   *policy.Engine
	Client   *rpc.Client
	RunDir   string // $AISH_RUN, where next.id/next.cmd are written
	Out      io.Writer

	entries []session.Entry
}

// Start records a new request and works on it.
func (a *Agent) Start(ctx context.Context, text string) error {
	if err := a.load(); err != nil {
		return err
	}
	if err := a.closePending(); err != nil {
		return err
	}
	cwd, _ := os.Getwd()
	inst := instructions(a.entries, cwd)
	for _, e := range inst {
		fmt.Fprintf(a.Out, "%s  (%s)%s\n", dim, tildePath(e.Path), reset)
	}
	if err := a.append(inst...); err != nil {
		return err
	}
	files := mentions(text, cwd)
	for _, e := range files {
		fmt.Fprintf(a.Out, "%s  %s%s\n", dim, mentionNote(e, cwd), reset)
	}
	if err := a.append(files...); err != nil {
		return err
	}
	if err := a.append(session.Entry{Kind: session.KindUser, Text: text, Cwd: cwd}); err != nil {
		return err
	}
	return a.drive(ctx)
}

// Resume records the result of the bash command with the given call id and
// continues.
func (a *Agent) Resume(ctx context.Context, id string, rc int) error {
	if err := a.load(); err != nil {
		return err
	}
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
	out, err := a.Client.WaitOutput(id, 10*time.Second)
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
func (a *Agent) closePending() error {
	for _, c := range pending(a.entries) {
		msg := "interrupted by the user"
		if c.Name == tools.Bash {
			if out, err := a.Client.WaitOutput(c.ID, 0); err == nil && out.Output != "" {
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
// sent, and instruction files read before it are read again.
func (a *Agent) load() error {
	es, err := a.Client.History()
	a.entries = session.Current(es)
	return err
}

func (a *Agent) append(es ...session.Entry) error {
	for i := range es {
		if es[i].Time.IsZero() {
			es[i].Time = time.Now()
		}
	}
	a.entries = append(a.entries, es...)
	return a.Client.Append(es...)
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
			fmt.Fprintf(a.Out, "%s[aish: stopped after %d steps; ask to continue]%s\n", dim, a.Cfg.MaxSteps, reset)
			return a.append(session.Entry{Kind: session.KindAssistant, Text: fmt.Sprintf("(stopped after %d steps)", a.Cfg.MaxSteps)})
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
	sp := startSpinner(a.Out)
	md := newMarkdown(sp, a.Out, a.Cfg)
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
		InputTokens: resp.InputTokens, OutputTokens: resp.OutputTokens,
	}
	for _, c := range resp.ToolCalls {
		e.ToolCalls = append(e.ToolCalls, session.ToolCall{ID: c.ID, Name: c.Name, Args: c.Args})
	}
	if resp.StopReason == "max_tokens" || resp.StopReason == "length" {
		fmt.Fprintf(a.Out, "%s[aish: reply cut at max_tokens]%s\n", dim, reset)
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
	req := llm.Request{
		System:   system(extra),
		Messages: Messages(entries, a.Cfg.MaxOutputBytes),
	}
	for _, t := range a.Tools.All() {
		if t.Hidden {
			continue
		}
		req.Tools = append(req.Tools, llm.ToolDef{Name: t.Name, Description: t.Desc, Schema: t.Schema()})
	}
	return req
}

// call executes one tool call. For bash it writes the command for the shell
// and reports handedOff; the result arrives with `aish agent resume`.
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

	cwd, _ := os.Getwd()
	in := policy.NewInput(t.Name, args, cwd)
	in.Server = t.Server
	d, err := a.Policy.Check(ctx, in)
	if err != nil {
		return false, err
	}
	asked := d.Action == policy.Ask
	if asked {
		if t.Name == tools.Bash {
			a.showBash(args, true)
		}
		d = a.ask(d)
	}
	if d.Action == policy.Deny {
		msg := "denied by policy"
		if d.Reason != "" {
			msg += ": " + d.Reason
		}
		if t.Name == tools.Bash && !asked {
			a.showBash(args, true)
		}
		fmt.Fprintf(a.Out, "%s  ✗ %s%s\n", red, msg, reset)
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
		return true, a.handOff(c.ID, cmd)
	}

	// External tools print live; the proxy folds that output like a command's.
	fmt.Fprintf(a.Out, "\x1b]6973;fold-start;%s\a", title)
	res, err := t.Execute(ctx, args, a.Out)
	fmt.Fprint(a.Out, "\x1b]6973;fold-end\a")
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return false, ctx.Err()
		}
		fmt.Fprintf(a.Out, "%s  ✗ %v%s\n", red, err, reset)
		return false, a.append(toolResult(c, strings.TrimSpace(res+"\n"+err.Error()), true))
	}
	if t.Run != nil {
		// Built-ins print nothing live: a one-line result is shown as is, a
		// longer one is kept for Ctrl+O.
		line := summary(res)
		if strings.Contains(strings.TrimSpace(res), "\n") {
			line = "(" + line
			if a.Client.Call(rpc.MethodFold, rpc.Fold{Title: title, Text: res}, nil) == nil {
				line += " · ctrl+o to expand"
			}
			line += ")"
		}
		fmt.Fprintf(a.Out, "%s  %s%s\n", dim, line, reset)
	}
	return false, a.append(toolResult(c, capture.Truncate(res, a.Cfg.MaxOutputBytes*4), false))
}

func (a *Agent) handOff(id, cmd string) error {
	if err := os.WriteFile(filepath.Join(a.RunDir, "next.id"), []byte(id+"\n"), 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(a.RunDir, "next.cmd"), []byte(cmd), 0o600)
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
	fmt.Fprintf(a.Out, "%s⚙%s %s %s\n", cyan, reset, t.Name, strings.Join(parts, " "))
	return strings.TrimSpace("⚙ " + t.Name + " " + strings.Join(parts, " "))
}

// showBash prints a bash command. Unless nl, the line is left open when the
// output will be folded from its first line: the agent-col marker tells the
// proxy where the command ends, and the proxy puts the status to its right.
func (a *Agent) showBash(args map[string]any, nl bool) {
	cmd, _ := args["command"].(string)
	lines := strings.Split(cmd, "\n")
	fmt.Fprintf(a.Out, "%s❯%s %s%s%s", cyan, reset, bold, strings.Join(lines, "\n  "), reset)
	cols := 0
	if f, ok := a.Out.(*os.File); ok {
		cols, _, _ = term.GetSize(int(f.Fd()))
	}
	if nl || a.Cfg.FoldLines != 0 || cols <= 0 {
		fmt.Fprintln(a.Out)
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
	long := 0
	if len(lines) > 1 || w > cols {
		long = 1
	}
	fmt.Fprintf(a.Out, "\x1b]6973;agent-col;%d;%d\a", col, long)
}

// ask lets the user decide an "ask" verdict on the terminal.
func (a *Agent) ask(d policy.Decision) policy.Decision {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return policy.Decision{Action: policy.Deny, Reason: "needs confirmation, no terminal: " + d.Reason}
	}
	q := "allow?"
	if d.Reason != "" {
		q = d.Reason + " — allow?"
	}
	fmt.Fprintf(a.Out, "%s%s [y/N] %s", bold, q, reset)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
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
