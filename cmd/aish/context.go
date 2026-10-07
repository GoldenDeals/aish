package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/inebotov/aish/internal/agent"
	"github.com/inebotov/aish/internal/config"
	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/rpc"
	"github.com/inebotov/aish/internal/session"
)

const contextUsage = "usage: aish context [--full]"

// contextCmd shows how full the context is and what fills it: by kind of
// journal entry and by tool. With --full it also prints the messages the
// model gets, one JSON line each, on stdout; the rest goes to stderr, so
// that `aish context --full > ctx.jsonl` keeps the JSONL clean.
func contextCmd(cfg config.Config, args []string) int {
	full, err := contextArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	client, err := rpc.FromEnv()
	if err != nil {
		return fail(err)
	}
	var st rpc.Status
	if err := client.Call(rpc.MethodStatus, nil, &st); err != nil {
		return fail(err)
	}
	es, err := client.History()
	if err != nil {
		return fail(err)
	}
	info := st.Info
	// What the next request would take, as in statusCmd: the profile of
	// the shell and the settings of this directory, as they are in force.
	cwd, _ := os.Getwd()
	a, err := inForce(cfg, cwd, nil, false)
	if err != nil {
		return fail(err)
	}
	cfg = a.cfg
	// Built as the agent builds them: masked and truncated, so this shows
	// no secret the model is not sent.
	msgs, err := agent.ContextMessages(es, cfg)
	if err != nil {
		return fail(err)
	}

	w := os.Stderr
	row := func(k, v string) { fmt.Fprintf(w, "  \x1b[2m%-16s\x1b[0m %s\n", k, v) }
	head := func(s string) { fmt.Fprintf(w, "\x1b[1m%s\x1b[0m\n", s) }

	head("context")
	ctx := session.Short(st.Tokens) + " tokens"
	if info.Window > 0 {
		ctx = fmt.Sprintf("%s / %s (%d%%)", session.Short(st.Tokens), session.Short(info.Window), st.Tokens*100/info.Window)
	}
	if !st.Measured && st.Tokens > 0 {
		ctx += ", estimated"
	}
	row("used", ctx)
	// The window the agent takes, as Proxy.prepare does.
	agentWindow := cfg.ContextWindow
	if agentWindow <= 0 {
		agentWindow = info.Window
	}
	row("compact_at", compactAt(cfg.CompactAt, agentWindow))
	row("entries", fmt.Sprintf("%d commands, %d requests since the last compact", st.Commands, st.Requests))
	row("session", fmt.Sprintf("%d tool calls, %d compacts, %s in (%s cached) / %s out tokens spent",
		st.ToolCalls, st.Compacts, session.Short(st.InputTokens), session.Short(st.CachedTokens), session.Short(st.OutputTokens)))
	dir := info.Dir
	if dir == "" { // a proxy started by an older aish
		dir = cfg.SessionsDir
	}
	journal := filepath.Join(dir, info.SessionID+".jsonl")
	if !info.Saved {
		journal = "not saved (aish clear save, aish new)"
	}
	row("journal", journal)

	if kinds := kindStats(session.Current(es), cfg.MaxOutputBytes); len(kinds) > 0 {
		head("by kind")
		printKinds(w, kinds)
	}
	if ts := toolStats(msgs); len(ts) > 0 {
		head("tools")
		printTools(w, ts)
	}
	if full {
		if err := writeContext(os.Stdout, msgs); err != nil {
			return fail(err)
		}
	}
	return 0
}

// contextArgs reads aish context [--full].
func contextArgs(args []string) (full bool, err error) {
	switch {
	case len(args) == 0:
		return false, nil
	case len(args) == 1 && args[0] == "--full":
		return true, nil
	}
	return false, errors.New(contextUsage)
}

// kindOrder is the order of the by kind table: what the user does, then
// the model, then what came along with requests.
var kindOrder = []string{
	session.KindShell, session.KindUser, session.KindAssistant, session.KindToolResult,
	session.KindInstructions, session.KindFile, session.KindSkill, session.KindContext, session.KindSummary,
}

// kindStat is one row of the by kind table; the last one, "total", sums
// the others.
type kindStat struct {
	kind           string
	entries, bytes int
}

// kindStats weighs the entries es, the current part of the journal, by
// kind, as session.Tokens estimates them; kinds without entries are left
// out. A kind kindOrder does not know goes after those it does, so that
// the total is still the sum of the rows.
func kindStats(es []session.Entry, maxOutput int) []kindStat {
	if len(es) == 0 {
		return nil
	}
	order := slices.Clone(kindOrder)
	by := map[string]*kindStat{}
	total := kindStat{kind: "total"}
	for _, e := range es {
		s := by[e.Kind]
		if s == nil {
			s = &kindStat{kind: e.Kind}
			by[e.Kind] = s
			if !slices.Contains(order, e.Kind) {
				order = append(order, e.Kind)
			}
		}
		n := session.EntryBytes(e, maxOutput)
		s.entries++
		s.bytes += n
		total.entries++
		total.bytes += n
	}
	var out []kindStat
	for _, k := range order {
		if s := by[k]; s != nil {
			out = append(out, *s)
		}
	}
	return append(out, total)
}

func printKinds(w io.Writer, rows []kindStat) {
	fmt.Fprintf(w, "  \x1b[2m%-16s %8s %10s %8s\x1b[0m\n", "kind", "entries", "bytes", "~tokens")
	for _, r := range rows {
		fmt.Fprintf(w, "  \x1b[2m%-16s\x1b[0m %8d %10d %8s\n", r.kind, r.entries, r.bytes, session.Short(r.bytes/4))
	}
}

// toolStat is one row of the tools table: bytes of the arguments of the
// calls and of their results as the model gets them.
type toolStat struct {
	name                         string
	calls, args, results, errors int
}

// toolStats weighs the tool calls in msgs and their results by tool, the
// heaviest results first. A result is the tool's its call names; one
// without a call in msgs, its call cut off by a clear in the middle of a
// request, goes by the name it carries.
func toolStats(msgs []llm.Message) []toolStat {
	by := map[string]*toolStat{}
	stat := func(name string) *toolStat {
		if by[name] == nil {
			by[name] = &toolStat{name: name}
		}
		return by[name]
	}
	names := map[string]string{} // call ID → tool
	for _, m := range msgs {
		for _, c := range m.ToolCalls {
			names[c.ID] = c.Name
			s := stat(c.Name)
			s.calls++
			s.args += len(c.Args)
		}
		for _, r := range m.ToolResults {
			name, ok := names[r.CallID]
			if !ok {
				name = r.Name
			}
			s := stat(name)
			s.results += len(r.Content)
			if r.IsError {
				s.errors++
			}
		}
	}
	var out []toolStat
	for _, s := range by {
		out = append(out, *s)
	}
	slices.SortFunc(out, func(a, b toolStat) int {
		return cmp.Or(cmp.Compare(b.results, a.results), cmp.Compare(a.name, b.name))
	})
	return out
}

func printTools(w io.Writer, rows []toolStat) {
	width := 16
	for _, r := range rows {
		width = max(width, len(r.name))
	}
	fmt.Fprintf(w, "  \x1b[2m%-*s %6s %10s %10s %6s\x1b[0m\n", width, "tool", "calls", "args", "results", "errors")
	for _, r := range rows {
		fmt.Fprintf(w, "  \x1b[2m%-*s\x1b[0m %6d %10d %10d %6d\n", width, r.name, r.calls, r.args, r.results, r.errors)
	}
}

// contextLine is one message of `aish context --full`. llm.Message has no
// JSON tags of its own, and its Raw, the provider's thinking and encrypted
// reasoning, is opaque and large: only its size is shown.
type contextLine struct {
	Role        string          `json:"role"`
	Text        string          `json:"text,omitempty"`
	ToolCalls   []contextCall   `json:"tool_calls,omitempty"`
	ToolResults []contextResult `json:"tool_results,omitempty"`
	Provider    string          `json:"provider,omitempty"`
	Model       string          `json:"model,omitempty"`
	RawBytes    int             `json:"raw_bytes,omitempty"`
}

type contextCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type contextResult struct {
	CallID  string `json:"call_id"`
	Name    string `json:"name"`
	Content string `json:"content"`
	IsError bool   `json:"is_error,omitempty"`
}

func contextLineOf(m llm.Message) contextLine {
	l := contextLine{Role: m.Role, Text: m.Text, Provider: m.Provider, Model: m.Model, RawBytes: len(m.Raw)}
	for _, c := range m.ToolCalls {
		args := c.Args
		if !json.Valid(args) {
			// As the model wrote them: an encoder error would cut the
			// output short at this call.
			args, _ = json.Marshal(string(args))
		}
		l.ToolCalls = append(l.ToolCalls, contextCall{ID: c.ID, Name: c.Name, Args: args})
	}
	for _, r := range m.ToolResults {
		l.ToolResults = append(l.ToolResults, contextResult{CallID: r.CallID, Name: r.Name, Content: r.Content, IsError: r.IsError})
	}
	return l
}

// writeContext prints msgs as JSONL, one message a line, < and & as they
// are: the output is read, not put into HTML.
func writeContext(w io.Writer, msgs []llm.Message) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for _, m := range msgs {
		if err := enc.Encode(contextLineOf(m)); err != nil {
			return err
		}
	}
	return nil
}
