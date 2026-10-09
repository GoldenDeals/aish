package agent

import (
	"slices"

	"github.com/GoldenDeals/aish/internal/session"
)

// A subagent works for the host's session, and what its turns cost is
// that session's spend: aish stats counts it. Each turn the API measured
// leaves a KindUsage entry in the host's journal, written from the
// subagent's goroutine through the journal's own lock, as the host's
// entries are: for one in the background during a later request or
// between requests. The entry is no part of the context: load leaves it
// out of the agent's entries, Messages skips it, session.Tokens counts
// nothing for it, and its tokens are in Usage, where nothing that takes
// InputTokens for the measure of the context looks.

// spent keeps what turn e of subagent s cost. Not once the shell has left
// the session the call was made in: clear and resume stop the subagents in
// the background only after the switch, and the next session would begin
// with their spend, a journal of nothing else.
func (s *subRun) spent(e session.Entry) {
	if s.journal == nil || e.InputTokens == 0 && e.OutputTokens == 0 {
		return // a turn cut off, or a provider that counts nothing
	}
	if s.journal.ID() != s.sess {
		return
	}
	// A write that fails costs aish stats a turn; the host's next entry
	// tells of the disk.
	_ = s.journal.Append(session.Entry{
		Kind: session.KindUsage, Time: e.Time, About: s.def.Name,
		Provider: e.Provider, Model: e.Model, Profile: e.Profile,
		Usage: &session.Usage{Input: e.InputTokens, Cached: e.CachedTokens, Output: e.OutputTokens},
	})
}

func isUsage(e session.Entry) bool { return e.Kind == session.KindUsage }

// withoutUsage is es without its KindUsage entries; es itself when it has
// none.
func withoutUsage(es []session.Entry) []session.Entry {
	if !slices.ContainsFunc(es, isUsage) {
		return es
	}
	return slices.DeleteFunc(slices.Clone(es), isUsage)
}

// ownEntries counts the entries of es other than KindUsage: those the
// agent tells a journal changed by.
func ownEntries(es []session.Entry) int {
	n := 0
	for _, e := range es {
		if !isUsage(e) {
			n++
		}
	}
	return n
}
