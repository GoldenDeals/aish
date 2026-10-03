package agent

import (
	"context"
	"fmt"
	"slices"

	"github.com/inebotov/aish/internal/capture"
	"github.com/inebotov/aish/internal/config"
	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/session"
)

// autoNote tells the model why it sums the session up unasked: the work
// goes on from the summary, and nobody is there to fill in what it leaves
// out.
const autoNote = "The context is compacted automatically, in the middle of the work: the request in progress " +
	"goes on right after this summary, without a word from the user. Say what of it is done, what is left " +
	"and the next step."

// minCut is how much of a result is kept however far it is cut: the head
// and the tail, with the exit status.
const minCut = 1000

// compactLimit is the context size, in tokens, past which the session is
// compacted before the agent's next turn: compact_at of the window, 0 when
// either is unknown or off.
func compactLimit(cfg config.Config) int {
	return int(cfg.CompactAt * float64(cfg.ContextWindow))
}

// autoCompact keeps the context under compact_at of the window before a
// turn, so that a long request does not fail on the API's limit halfway:
// it sums the session up, as `aish compact` does, and the request goes on
// from the summary. A second summary right after one would hold about the
// same: then what keeps the context above the limit is what the one turn
// since brought, and its results are cut instead. A reply the window cut
// (windowFull) is summed up whatever the estimate, and with an unknown
// window too: the API counted the window full. A summary that fails past
// compact_at is not asked for again till the next request: every try sends
// the whole history.
func (a *Agent) autoCompact(ctx context.Context) error {
	limit := compactLimit(a.Cfg)
	tokens := a.contextTokens(a.entries)
	cur := session.Current(a.entries)
	if a.windowFull && !slices.ContainsFunc(cur, func(e session.Entry) bool { return e.Kind == session.KindAssistant }) {
		// The reply is gone from the context, and the window it filled with
		// it: `aish compact` or a clear came since.
		a.windowFull = false
	}
	full := a.windowFull && a.Cfg.CompactAt > 0
	if !full && (limit <= 0 || tokens <= limit) {
		return nil
	}
	if res := soleTurnResults(cur); res != nil && !full {
		cutResults(res, tokens-limit)
		fmt.Fprintf(a.UI, "%s[aish: context %s past compact_at right after a summary; the model gets the last output cut]%s\n",
			dim, session.Short(tokens), reset)
		return nil
	}
	if a.compactFailed && !full {
		return nil
	}

	if full {
		fmt.Fprintf(a.UI, "%sthe context window is full, compacting…%s\n", dim, reset)
	} else {
		fmt.Fprintf(a.UI, "%scontext at %d%% of the window, compacting…%s\n", dim, tokens*100/a.Cfg.ContextWindow, reset)
	}
	cwd := requestCwd(a.entries, a.exec.Dir)
	tail := requestTail(cur)
	sum, err := a.summarize(ctx, autoNote, cwd)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// The turn may still fit: the size is an estimate. A full window
		// has the summary asked for again, as it was.
		again := ""
		if !full {
			a.compactFailed = true
			again = "; not tried again in this request"
		}
		fmt.Fprintf(a.UI, "%s[aish: could not compact: %s%s]%s\n", dim, llm.Short(err), again, reset)
		return nil
	}
	if err := a.append(sum); err != nil {
		return err
	}
	a.windowFull, a.compactFailed = false, false
	// As after `aish compact`, instruction files are read again: everything
	// before the summary is gone for the model, and the request goes on.
	inst := instructions(session.Current(a.entries), cwd)
	for _, e := range inst {
		fmt.Fprintf(a.UI, "%s  (%s)%s\n", dim, tildePath(e.Path), reset)
	}
	if err := a.append(inst...); err != nil {
		return err
	}
	if err := a.append(tail...); err != nil {
		return err
	}
	after := a.contextTokens(a.entries)
	fmt.Fprintf(a.UI, "%scompacted: %s → %s tokens (aish session show prints the summary)%s\n",
		dim, session.Short(tokens), session.Short(after), reset)
	if limit > 0 && after > limit {
		fmt.Fprintf(a.UI, "%s[aish: still past compact_at: the summary, the instructions or the request are too big for the window]%s\n", dim, reset)
	}
	return nil
}

// requestTail is a copy of the request made since the last turn, when no
// turn has answered it yet: the files it mentions and the request itself,
// sent after the summary as typed rather than retold by it. Instruction
// files are left out: they are read again.
func requestTail(cur []session.Entry) []session.Entry {
	from := len(cur)
	for ; from > 0; from-- {
		if k := cur[from-1].Kind; k == session.KindShell || k == session.KindAssistant || k == session.KindToolResult || k == session.KindSummary {
			break
		}
	}
	var tail []session.Entry
	request := false
	for _, e := range cur[from:] {
		if e.Kind == session.KindInstructions {
			continue
		}
		request = request || e.Kind == session.KindUser
		tail = append(tail, e)
	}
	if !request {
		return nil
	}
	return tail
}

// soleTurnResults is the results of the last turn when it is the only turn
// since a summary and nothing came after them: a summary was made, the
// model answered it with tool calls, and their results took the context
// past the limit.
func soleTurnResults(cur []session.Entry) []session.Entry {
	if len(cur) == 0 || cur[0].Kind != session.KindSummary {
		return nil
	}
	last, turns := -1, 0
	for i, e := range cur {
		if e.Kind == session.KindAssistant {
			last, turns = i, turns+1
		}
	}
	if turns != 1 || last == len(cur)-1 {
		return nil
	}
	for _, e := range cur[last+1:] {
		if e.Kind != session.KindToolResult {
			return nil
		}
	}
	return cur[last+1:]
}

// cutResults shortens res, in place, by about tokens, each in proportion
// to its size. The entries are the agent's: the journal keeps the results
// whole.
func cutResults(res []session.Entry, tokens int) {
	total := 0
	for _, e := range res {
		total += len(e.Output)
	}
	keep := total - tokens*4 // Tokens counts four bytes a token
	if total == 0 || keep >= total {
		return
	}
	for i := range res {
		n := max(len(res[i].Output)*max(keep, 0)/total, minCut)
		res[i].Output = capture.Truncate(res[i].Output, n)
	}
}
