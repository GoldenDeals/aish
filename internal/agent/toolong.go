package agent

import (
	"slices"

	"github.com/GoldenDeals/aish/internal/session"
)

// summaryKeep is about how much of each output is sent for a summary the
// whole history did not fit the window with: enough for the model to tell
// what a command showed.
const summaryKeep = 4096

// compactable tells whether a summary can make the context smaller: there
// is a turn in it, as autoCompact wants for a full window.
func compactable(es []session.Entry) bool {
	return slices.ContainsFunc(session.Current(es), func(e session.Entry) bool { return e.Kind == session.KindAssistant })
}

// cutOutputs is a copy of es with the outputs in the context, of tools and
// of the user's commands, cut to about summaryKeep bytes each on average;
// nil when they are no bigger than that. The journal and the agent's
// entries keep them whole: only a summary is asked for with them cut.
func cutOutputs(es []session.Entry) []session.Entry {
	es = slices.Clone(es)
	cur := session.Current(es) // a part of es: what is cut in it is cut in es
	var at []int
	var outs []session.Entry
	total := 0
	for i, e := range cur {
		if e.Kind == session.KindToolResult || e.Kind == session.KindShell {
			at = append(at, i)
			outs = append(outs, e)
			total += len(e.Output)
		}
	}
	over := total - len(outs)*summaryKeep
	if over <= 0 {
		return nil
	}
	cutResults(outs, over)
	for k, i := range at {
		cur[i].Output = outs[k].Output
	}
	return es
}
