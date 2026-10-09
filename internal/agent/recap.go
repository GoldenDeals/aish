package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/GoldenDeals/aish/internal/capture"
	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/session"
)

// A recap retells the whole session, not the context: the journal before
// each summary and clear too. The model gets it as a transcript, one
// message of text, not as the conversation it was: the parts before a
// clear may hold tool calls left without results, which the API rejects,
// and a recap needs no tools. Nothing of it is recorded: what the user
// reads is all there is of it.

const recapSystem = "You retell to the user what happened in their aish session: a terminal where commands run " +
	"in the user's shell and requests in plain language go to an LLM assistant built into it."

const recapPrompt = `Above is the transcript of a whole aish session. The user asks for a recap of it, to recall what happened. Reply with the recap only.

Write it in the language the user writes in, in the order things happened, and include:
1. What the user asked for over the session, quoting the user's own words where they matter.
2. What was done: commands that mattered and what they showed, files read, created or changed (with paths), decisions and why.
3. Errors met and how they were fixed or left.
4. Where the session stands: what is done, what is left open.

Leave out what does not matter: routine output, dead ends that taught nothing.

In the transcript, <clear/> is where the user cleared the screen: from there on the assistant knew nothing of what came before. <compacted/> is where the context was compacted: from there on the assistant went on from its own summary of what came before. A <summary> stands in for what came before it, back to the previous <summary> or <clear/>: that part is left out as too long.`

const condensePrompt = `Above is a part of the transcript of an aish session, too long to be recapped along with the rest: the recap of the whole session will be written from your summary of this part in its place. Reply with the summary only.

Write it in the language the user writes in, briefly and in the order things happened, and include what the user asked for, what was done (commands that mattered and what they showed, files with paths, decisions), errors met and how they ended, and where the part leaves off.`

const (
	clearMark     = "<clear/>"
	compactedMark = "<compacted/>"
	// recapTries is how many times the transcript is sent, made smaller
	// each time the API finds it past the window, though the estimate
	// said it fits.
	recapTries = 3
	// summedTokens is about what the summary of a part comes to: summing
	// up must free that much more than the transcript is over by.
	summedTokens = 2000
)

// errRecapWindow is a reply the window cut before it said anything: the
// transcript did not fit.
var errRecapWindow = errors.New("the recap was cut: the context window is full")

// Recap retells the whole session on the screen. The journal, the
// entries of the agent and the context stay as they were: the recap goes
// nowhere but to the screen, and what the call cost is not recorded.
func (a *Agent) Recap(ctx context.Context) error {
	a.dropWork() // as Compact does: the shell is at its prompt
	parts := recapParts(a.Journal.Entries())
	if !slices.ContainsFunc(parts, func(p recapPart) bool { return len(p.es) > 0 }) {
		return errors.New("nothing to recap")
	}
	mask, err := NewMasker(a.Cfg.MaskDefaults, a.Cfg.Mask)
	if err != nil { // config.Load rejects these; keep the built-in ones
		fmt.Fprintf(a.UI, "%s[aish: %v]%s\n", dim, err, reset)
		mask, _ = NewMasker(true, nil)
	}
	budget := a.recapBudget()
	for try := 1; ; try++ {
		pieces, err := a.fitRecap(ctx, parts, budget, mask)
		if err == nil {
			err = a.tellRecap(ctx, pieces)
		}
		if try == recapTries || ctx.Err() != nil || !errors.Is(err, errRecapWindow) && !llm.PromptTooLong(err) {
			return err
		}
		// The API counts for sure. Without a budget nothing was summed
		// up, and pieces is the whole transcript.
		if budget <= 0 {
			budget = recapTokens(pieces)
		}
		budget = budget * 2 / 3
	}
}

// recapPart is the journal between two boundaries, a summary or a clear:
// lead is the one it starts with, nil for the first part.
type recapPart struct {
	lead *session.Entry
	es   []session.Entry
}

func recapParts(es []session.Entry) []recapPart {
	parts := []recapPart{{}}
	for i, e := range es {
		if e.Kind == session.KindSummary || e.Kind == session.KindClear {
			parts = append(parts, recapPart{lead: &es[i]})
			continue
		}
		last := &parts[len(parts)-1]
		last.es = append(last.es, e)
	}
	return parts
}

// recapBudget is how many tokens the transcript may take: the window less
// the reply and the prompt; 0 when the window is unknown, and only the API
// can tell that the transcript does not fit.
func (a *Agent) recapBudget() int {
	w := a.Cfg.ContextWindow
	if w <= 0 {
		return 0
	}
	reply := min(int(a.Provider.MaxTokens(a.Cfg.Effort)), w/2)
	return w - reply - (len(recapSystem)+len(recapPrompt))/4
}

// fitRecap is the transcript of parts, piece by piece, within budget
// tokens unless it is 0. The whole of it if it fits; else, as the context
// had it, the parts a summary was made of give way to the last summary of
// them; else its earliest pieces are summed up, as many as one call takes
// at a time, till the rest fits.
func (a *Agent) fitRecap(ctx context.Context, parts []recapPart, budget int, mask *Masker) ([]string, error) {
	pieces := a.recapPieces(parts, false, mask)
	whole := recapTokens(pieces)
	if budget <= 0 || whole <= budget {
		return pieces, nil
	}
	if slices.ContainsFunc(parts, func(p recapPart) bool { return p.lead != nil && p.lead.Kind == session.KindSummary }) {
		fmt.Fprintf(a.UI, "%s[aish: the session, ~%s tokens, is past the context window: its compacted parts go by their summaries]%s\n",
			dim, session.Short(whole), reset)
		pieces = a.recapPieces(parts, true, mask)
		if recapTokens(pieces) <= budget {
			return pieces, nil
		}
	}
	return a.condense(ctx, pieces, budget)
}

// recapPieces is the transcript of parts: a piece for each message, as
// Messages makes them, and for each boundary. bySummaries leaves out the
// parts a summary was made of, which the latest summary of them stands
// for: a clear between them ends what a summary after it is of.
func (a *Agent) recapPieces(parts []recapPart, bySummaries bool, mask *Masker) []string {
	var out []string
	for i, p := range parts {
		summed := bySummaries && i+1 < len(parts) && parts[i+1].lead.Kind == session.KindSummary
		switch {
		case p.lead == nil:
		case p.lead.Kind == session.KindClear:
			out = append(out, clearMark)
		case !bySummaries:
			out = append(out, compactedMark)
		case !summed:
			out = append(out, "<summary>\n"+p.lead.Text+"\n</summary>")
		}
		if summed {
			continue
		}
		for _, m := range Messages(p.es, a.Cfg.MaxOutputBytes, mask) {
			out = append(out, messageText(m))
		}
	}
	return out
}

// messageText is a message of the conversation as a piece of the
// transcript.
func messageText(m llm.Message) string {
	var b strings.Builder
	b.WriteString("<" + m.Role + ">\n")
	if m.Text != "" {
		b.WriteString(m.Text + "\n")
	}
	for _, c := range m.ToolCalls {
		fmt.Fprintf(&b, "<tool_call name=%q>\n%s\n</tool_call>\n", c.Name, c.Args)
	}
	for _, r := range m.ToolResults {
		failed := ""
		if r.IsError {
			failed = ` error="true"`
		}
		fmt.Fprintf(&b, "<tool_result name=%q%s>\n%s\n</tool_result>\n", r.Name, failed, r.Content)
	}
	b.WriteString("</" + m.Role + ">")
	return b.String()
}

// recapTokens counts the pieces as session.Tokens does: four bytes a token.
func recapTokens(pieces []string) int {
	n := 0
	for _, p := range pieces {
		n += len(p) + 2
	}
	return n / 4
}

// condense sums up the earliest pieces, a call for as many as the window
// takes at once with room to spare, till the rest fits budget. A piece
// bigger than that is cut, its head and tail kept.
func (a *Agent) condense(ctx context.Context, pieces []string, budget int) ([]string, error) {
	chunk := budget * 3 / 4
	pieces = slices.Clone(pieces)
	for i := range pieces {
		pieces[i] = capture.Truncate(pieces[i], chunk*4)
	}
	for from := 0; ; from++ {
		total := recapTokens(pieces)
		if total <= budget {
			return pieces, nil
		}
		if from == len(pieces) {
			return nil, fmt.Errorf("the session is too long to recap: ~%s tokens past the context window once summed up", session.Short(total-budget))
		}
		need := total - budget + summedTokens
		to, n := from, 0
		for to < len(pieces) && n < need {
			t := recapTokens(pieces[to : to+1])
			if to > from && n+t > chunk {
				break
			}
			n, to = n+t, to+1
		}
		if from == 0 {
			fmt.Fprintf(a.UI, "%s[aish: ~%s tokens of the session are past the context window: its earliest part is summed up first]%s\n",
				dim, session.Short(total-budget), reset)
		}
		sum, err := a.sumUp(ctx, pieces[from:to])
		if err != nil {
			return nil, err
		}
		pieces = slices.Replace(pieces, from, to, "<summary>\n"+sum+"\n</summary>")
	}
}

// sumUp asks the model for a summary of pieces, the transcript of a part
// of the session.
func (a *Agent) sumUp(ctx context.Context, pieces []string) (string, error) {
	cols, _ := a.UI.Size()
	sp := startSpinner(a.UI, cols > 0)
	resp, err := a.complete(ctx, recapRequest(pieces, condensePrompt), nil, func(note string) {
		sp.Stop()
		fmt.Fprintf(a.UI, "%s%s%s\n", dim, note, reset)
		sp = startSpinner(a.UI, cols > 0)
	})
	sp.Stop()
	if err != nil {
		return "", err
	}
	if resp.StopReason == llm.StopContextWindow {
		return "", errRecapWindow
	}
	text := strings.TrimSpace(resp.Text)
	if text == "" {
		return "", errors.New("the model returned an empty summary of a part of the session")
	}
	return text, nil
}

// tellRecap streams the recap of the transcript to the screen, rendered
// as the agent's answers are.
func (a *Agent) tellRecap(ctx context.Context, pieces []string) error {
	cols, _ := a.UI.Size()
	sp := startSpinner(a.UI, cols > 0)
	md := newMarkdown(sp, a.UI.Size, a.Cfg)
	shown := false
	resp, err := a.complete(ctx, recapRequest(pieces, recapPrompt), func(s string) {
		io.WriteString(md, s)
		shown = shown || s != ""
	}, func(note string) {
		md.Flush()
		sp.Stop()
		fmt.Fprintf(a.UI, "%s%s%s\n", dim, note, reset)
		sp = startSpinner(a.UI, cols > 0)
		md = newMarkdown(sp, a.UI.Size, a.Cfg)
	})
	md.Flush()
	sp.Stop()
	if err != nil {
		return err
	}
	switch {
	case resp.StopReason == llm.StopContextWindow && !shown:
		return errRecapWindow
	case resp.StopReason == llm.StopContextWindow:
		fmt.Fprintf(a.UI, "%s[aish: recap cut: the context window is full]%s\n", dim, reset)
	case resp.StopReason == llm.StopMaxTokens:
		fmt.Fprintf(a.UI, "%s[aish: recap cut at max_tokens]%s\n", dim, reset)
	case resp.StopReason == llm.StopRefusal:
		fmt.Fprintf(a.UI, "%s[aish: the model declined to answer]%s\n", dim, reset)
	case !shown:
		return errors.New("the model returned an empty recap")
	}
	return nil
}

// recapRequest is the transcript with prompt after it, in one message.
func recapRequest(pieces []string, prompt string) llm.Request {
	text := "<transcript>\n" + strings.Join(pieces, "\n\n") + "\n</transcript>\n\n" + prompt
	return llm.Request{System: recapSystem, Messages: []llm.Message{{Role: llm.RoleUser, Text: text}}}
}
