package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/inebotov/aish/internal/session"
	"github.com/inebotov/aish/internal/tools"
)

const compactPrompt = `Your context is about to be replaced with a summary of this session, written by you now. Do not call tools; reply with the summary only.

The summary must let you continue the work as if nothing was lost. Write it in the language the user writes in, and include:
1. What the user asked for over the session, the current task and its goal, quoting the user's own words where they matter.
2. What was done: commands that mattered and what they showed, files read, created or changed (with paths), decisions and why.
3. Errors met and how they were fixed or left.
4. Facts about the environment learned along the way: directories, project layout, tools, versions, configuration.
5. The current state: where the work stopped, what is pending, the next step if there is one.

Leave out what no longer matters: routine output, dead ends that taught nothing.`

func summaryBlock(e session.Entry) string {
	return "<summary>\nThis session continues from an earlier part, which was summarized to save context:\n\n" +
		e.Text + "\n</summary>"
}

// Compact asks the model to sum the session up and records the summary: from
// then on it is sent instead of everything before it. Focus says what the
// summary should keep in particular; ex is the shell the user asked from.
func (a *Agent) Compact(ctx context.Context, focus string, ex tools.Exec) error {
	a.exec = ex
	a.load(true)
	if len(a.entries) == 0 || len(a.entries) == 1 && a.entries[0].Kind == session.KindSummary {
		return errors.New("nothing to compact")
	}
	if err := a.closePending(ctx); err != nil {
		return err
	}
	before := a.contextTokens(a.entries)
	var note string
	if focus = strings.TrimSpace(focus); focus != "" {
		note = "The user asks the summary to focus on: " + focus
	}
	sum, err := a.summarize(ctx, note, ex.Dir)
	if err != nil {
		return err
	}
	if err := a.append(sum); err != nil {
		return err
	}
	after := a.contextTokens(a.entries)
	fmt.Fprintf(a.UI, "%scompacted: %s → %s tokens (aish session show prints the summary)%s\n",
		dim, session.Short(before), session.Short(after), reset)
	return nil
}

// summarize asks the model to sum up the entries the agent holds, with
// note added to the prompt, and returns the summary unrecorded.
func (a *Agent) summarize(ctx context.Context, note, cwd string) (session.Entry, error) {
	prompt := compactPrompt
	if note != "" {
		prompt += "\n\n" + note
	}
	es := append(a.entries[:len(a.entries):len(a.entries)], session.Entry{Kind: session.KindUser, Text: prompt, Cwd: cwd, Time: time.Now()})
	// The tools stay in the request: the history has calls to them.
	req := a.request(es)

	cols, _ := a.UI.Size()
	sp := startSpinner(a.UI, cols > 0)
	resp, err := a.Provider.Complete(ctx, req, nil)
	sp.Stop()
	if err != nil {
		return session.Entry{}, err
	}
	text := strings.TrimSpace(resp.Text)
	if text == "" {
		return session.Entry{}, errors.New("the model returned an empty summary")
	}
	return session.Entry{Kind: session.KindSummary, Text: text, Cwd: cwd}, nil
}
