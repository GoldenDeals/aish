package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/session"
	"github.com/inebotov/aish/internal/tools"
)

// Deferred tools (tools.Hidden: those of MCP servers by default) are not
// sent to the model with their schemas: a user with a few servers has
// hundreds of them, tens of thousands of tokens in every request. The
// system prompt names them, and the model loads the ones it needs with
// tool_search; from the next turn on they are in the request as any other
// tool. Which ones are loaded is read from the entries the messages are
// made of, so the journal is all it takes to send the same tools again.

const (
	toolSearch = "tool_search"
	// searchMax is how many tools a search loads when the model does not
	// say.
	searchMax = 5
	// instructionsMax bounds what the instructions of one server take of
	// the system prompt.
	instructionsMax = 4 << 10
	// sentenceMax bounds the description of a tool in tool_search's result.
	sentenceMax = 200
)

// searchTool loads deferred tools for the model. It is not in the registry:
// the agent offers it while it defers tools (deferring), so the user has no
// such command.
type searchTool struct{ a *Agent }

func (searchTool) Name() string { return toolSearch }

func (searchTool) Desc() string {
	return "Loads deferred tools. The system prompt lists them by name under \"Deferred tools\", but their schemas " +
		"are not loaded, and a tool cannot be called before it is. Once this call returns, the tools it loaded can " +
		"be called like any other tool.\n\n" +
		"Query forms:\n" +
		"- \"select:NAME,NAME\" loads these tools by their exact names, as listed: use it when you know the names.\n" +
		"- Keywords (\"merge request comments\") load the tools whose names and descriptions match them best, " +
		"up to max_results."
}

func (searchTool) Args() []tools.Arg {
	return []tools.Arg{
		{Name: "query", Type: "string", Required: true,
			Desc: "\"select:NAME,NAME\" for tools by name, or keywords to search their names and descriptions"},
		{Name: "max_results", Type: "integer", Flag: true,
			Desc: fmt.Sprintf("How many tools a keyword search loads at most (default %d)", searchMax)},
	}
}

func (s searchTool) Schema() map[string]any { return tools.Schema(s.Args()) }

// Execute answers with a line "loaded: A, B", which the agent reads the
// loaded tools back from (loaded), and a line on each of them. Nothing
// loaded is an error.
func (s searchTool) Execute(_ context.Context, _ tools.Exec, args map[string]any, _ io.Writer) (string, error) {
	query, _ := args["query"].(string)
	query = strings.TrimSpace(query)
	if query == "" {
		return "", errors.New("query is empty")
	}
	deferred := s.a.deferred()
	var found []tools.Tool
	var notes []string
	if list, ok := cutFold(query, "select:"); ok {
		found, notes = s.pick(deferred, strings.Split(list, ","))
	} else if t := byName(deferred, query); t != nil {
		found = []tools.Tool{t}
	} else {
		found = rank(deferred, query, maxResults(args))
	}
	var b strings.Builder
	if len(found) > 0 {
		names := make([]string, len(found))
		for i, t := range found {
			names[i] = t.Name()
		}
		fmt.Fprintf(&b, "loaded: %s\n", strings.Join(names, ", "))
		for _, t := range found {
			if d := firstSentence(t.Desc()); d != "" {
				fmt.Fprintf(&b, "%s: %s\n", t.Name(), d)
			} else {
				fmt.Fprintf(&b, "%s\n", t.Name())
			}
		}
	}
	for _, n := range notes {
		fmt.Fprintf(&b, "%s\n", n)
	}
	out := strings.TrimRight(b.String(), "\n")
	switch {
	case len(found) > 0:
		return out, nil
	case len(notes) > 0:
		return out, errors.New("no tool loaded")
	}
	return out, fmt.Errorf("no deferred tool matches %q; the system prompt lists them all under Deferred tools", query)
}

// pick is the deferred tools of names, in their order, and a note on each
// name that gives none.
func (s searchTool) pick(deferred []tools.Tool, names []string) (found []tools.Tool, notes []string) {
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		t := byName(deferred, n)
		switch {
		case t != nil:
			// By name: a tool may be of a type == panics on.
			if byName(found, t.Name()) == nil {
				found = append(found, t)
			}
		case s.has(n):
			notes = append(notes, "already available: "+n)
		default:
			notes = append(notes, "not found: "+n)
		}
	}
	return found, notes
}

// has tells whether name is a tool the model has without loading it.
func (s searchTool) has(name string) bool {
	t, ok := s.a.Tools.Get(name)
	return ok && !tools.IsHidden(t)
}

// byName is the tool called name, or by that name in another case.
func byName(list []tools.Tool, name string) tools.Tool {
	var fold tools.Tool
	for _, t := range list {
		if t.Name() == name {
			return t
		}
		if fold == nil && strings.EqualFold(t.Name(), name) {
			fold = t
		}
	}
	return fold
}

func maxResults(args map[string]any) int {
	n := 0
	switch v := args["max_results"].(type) {
	case float64:
		n = int(v)
	case int:
		n = v
	case string:
		n, _ = strconv.Atoi(v)
	}
	if n <= 0 {
		return searchMax
	}
	return n
}

// rank is the tools of list that match the words of query best, at most
// limit: a word among those of a tool's name weighs 2, among those of its
// description 1. Tools that match nothing are left out; equal ones keep
// their order in list.
func rank(list []tools.Tool, query string, limit int) []tools.Tool {
	var want []string
	for _, w := range words(query) {
		if !slices.Contains(want, w) {
			want = append(want, w)
		}
	}
	type scored struct {
		t     tools.Tool
		score int
	}
	var hits []scored
	for _, t := range list {
		name, desc := nameWords(t.Name()), words(t.Desc())
		score := 0
		for _, w := range want {
			if matches(name, w) {
				score += 2
			}
			if matches(desc, w) {
				score++
			}
		}
		if score > 0 {
			hits = append(hits, scored{t, score})
		}
	}
	slices.SortStableFunc(hits, func(a, b scored) int { return b.score - a.score })
	var out []tools.Tool
	for _, h := range hits[:min(len(hits), limit)] {
		out = append(out, h.t)
	}
	return out
}

// words are the words of s in lower case: runs of letters and digits.
func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// nameWords are the parts of a tool's name, split at _ and -, and at a
// change to upper case too: servers name their tools get_issue as well as
// getIssue.
func nameWords(name string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(name, func(r rune) bool { return r == '_' || r == '-' }) {
		start := 0
		rs := []rune(part)
		for i := 1; i < len(rs); i++ {
			if unicode.IsUpper(rs[i]) && unicode.IsLower(rs[i-1]) {
				out = append(out, strings.ToLower(string(rs[start:i])))
				start = i
			}
		}
		out = append(out, strings.ToLower(string(rs[start:])))
	}
	return out
}

// matches tells whether w is among list: the same word, or one that only
// ends differently (issue, issues), the shorter of them four letters at
// least.
func matches(list []string, w string) bool {
	for _, x := range list {
		short, long := x, w
		if len(short) > len(long) {
			short, long = long, short
		}
		if x == w || utf8.RuneCountInString(short) >= 4 && strings.HasPrefix(long, short) {
			return true
		}
	}
	return false
}

// firstSentence is the start of a description, for a line of its own.
func firstSentence(desc string) string {
	s := strings.Join(strings.Fields(desc), " ")
	if i := strings.Index(s, ". "); i >= 0 {
		s = s[:i+1]
	}
	if utf8.RuneCountInString(s) > sentenceMax {
		s = string([]rune(s)[:sentenceMax-1]) + "…"
	}
	return s
}

// cutFold is s without prefix, matched in any case.
func cutFold(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		return s[len(prefix):], true
	}
	return "", false
}

// deferred is the agent's deferred tools in the order of its registry.
func (a *Agent) deferred() []tools.Tool {
	var out []tools.Tool
	for _, t := range a.Tools.All() {
		if tools.IsHidden(t) {
			out = append(out, t)
		}
	}
	return out
}

// deferring tells whether the agent defers its hidden tools: it has some,
// and tool_search is its own. A tool of the user's by that name keeps the
// name, and the hidden tools go to the model whole then.
func (a *Agent) deferring() bool {
	if _, taken := a.Tools.Get(toolSearch); taken {
		return false
	}
	return slices.ContainsFunc(a.Tools.All(), tools.IsHidden)
}

// tool is the tool a call names: one of the registry or, while the agent
// defers tools, tool_search. A hidden tool called without being loaded is
// in the registry, and runs.
func (a *Agent) tool(name string) (tools.Tool, bool) {
	if t, ok := a.Tools.Get(name); ok {
		return t, true
	}
	if name == toolSearch && a.deferring() {
		return searchTool{a}, true
	}
	return nil, false
}

// loaded is the deferred tools the model has loaded in entries, in the
// order it did: those of the "loaded:" lines of tool_search's results, and
// any it called, so that no call in the history is without the tool's
// definition (a post-tool hook may have changed the result, say). Entries
// before a summary or a clear are not sent, and their loads go with them.
// A tool no longer in the registry (its server is gone) is left out.
func (a *Agent) loaded(entries []session.Entry) []tools.Tool {
	var out []tools.Tool
	seen := map[string]bool{}
	add := func(name string) {
		if t, ok := a.Tools.Get(name); ok && tools.IsHidden(t) && !seen[name] {
			seen[name] = true
			out = append(out, t)
		}
	}
	for _, e := range session.Current(entries) {
		switch {
		case e.Kind == session.KindAssistant:
			for _, c := range e.ToolCalls {
				add(c.Name)
			}
		case e.Kind == session.KindToolResult && e.ToolName == toolSearch && !e.IsError:
			first, _, _ := strings.Cut(e.Output, "\n")
			if list, ok := strings.CutPrefix(first, "loaded:"); ok {
				for _, n := range strings.Split(list, ",") {
					add(strings.TrimSpace(n))
				}
			}
		}
	}
	return out
}

// toolDefs is the tools of a request with entries: those not deferred in
// the order of the registry, then tool_search, then the loaded ones in the
// order they were loaded. A load adds to the end, so the start of the list
// stays as it was; the tools are the first thing Anthropic caches, though,
// and the turn after a load writes the cache anew: the cost of that turn,
// not of every one.
func (a *Agent) toolDefs(entries []session.Entry) []llm.ToolDef {
	defers := a.deferring()
	var defs []llm.ToolDef
	add := func(t tools.Tool) {
		defs = append(defs, llm.ToolDef{Name: t.Name(), Description: t.Desc(), Schema: t.Schema()})
	}
	for _, t := range a.Tools.All() {
		if !defers || !tools.IsHidden(t) {
			add(t)
		}
	}
	if defers {
		add(searchTool{a})
		for _, t := range a.loaded(entries) {
			add(t)
		}
	}
	return defs
}

// toolsPrompt is what the system prompt says of the tools: the
// instructions of the MCP servers and the names of the deferred tools, each
// part only when there is something to say. It follows the registry and
// nothing else, so the prompt stays cached while the tools are the same.
func (a *Agent) toolsPrompt() string {
	var servers []string // in the order of their first tools
	texts := map[string]string{}
	var groups []string // of the deferred tools, by server
	names := map[string][]string{}
	defers := a.deferring()
	for _, t := range a.Tools.All() {
		s := tools.ServerOf(t)
		if _, seen := texts[s]; s != "" && !seen {
			texts[s] = strings.TrimSpace(tools.InstructionsOf(t))
			if texts[s] != "" {
				servers = append(servers, s)
			}
		}
		if defers && tools.IsHidden(t) {
			if s == "" {
				s = "other"
			}
			if _, seen := names[s]; !seen {
				groups = append(groups, s)
			}
			names[s] = append(names[s], t.Name())
		}
	}
	var parts []string
	if len(servers) > 0 {
		var b strings.Builder
		b.WriteString("# MCP Server Instructions\n" +
			"The following MCP servers have provided instructions for how to use their tools:")
		for _, s := range servers {
			fmt.Fprintf(&b, "\n\n## %s\n%s", s, clip(texts[s], instructionsMax))
		}
		parts = append(parts, b.String())
	}
	if len(groups) > 0 {
		var b strings.Builder
		b.WriteString("# Deferred tools\n" +
			"The tools below are available, but their schemas are not loaded, so they cannot be called yet. " +
			"Load the ones you need with tool_search: \"select:NAME,NAME\" by name, or keywords that search their " +
			"names and descriptions. Once loaded, they are called like any other tool.\n")
		for _, s := range groups {
			fmt.Fprintf(&b, "\n%s (%d): %s", s, len(names[s]), strings.Join(names[s], ", "))
		}
		parts = append(parts, b.String())
	}
	return strings.Join(parts, "\n\n")
}

// clip cuts s to at most limit bytes, and marks the cut.
func clip(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimRightFunc(s[:cut], unicode.IsSpace) + "…"
}
