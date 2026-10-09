package policy

import (
	"slices"
	"strings"
)

// hinter is a checker whose rules carry texts for the model: what a rule
// forbids and what to do instead.
type hinter interface{ hints() []string }

// Hints are the texts the rules in force carry for the model, @hint of
// Cedar and the hints of [policy], said in the system prompt so that the
// model keeps to them from the start rather than learns them from being
// refused. In the order of the checkers: the built-in policy, the rules
// of [policy] (deny, ask, write_outside_home), then the Cedar
// directories, each in the order of its files; trimmed, each once. A subagent's engine has the host's.
// A hint is said whatever the conditions of its rule: its author writes
// them into the text.
func (e *Engine) Hints() []string {
	if e == nil {
		return nil
	}
	var out []string
	for _, c := range e.checkers {
		h, ok := c.(hinter)
		if !ok {
			continue
		}
		for _, text := range h.hints() {
			if text = strings.TrimSpace(text); text != "" && !slices.Contains(out, text) {
				out = append(out, text)
			}
		}
	}
	return out
}
