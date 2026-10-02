package agent

import "github.com/inebotov/aish/internal/session"

// ownRaw is es with the Raw of replies from another profile dropped:
// their thinking and encrypted reasoning are bound to the account that
// made them. es itself is returned when there is nothing to drop.
func ownRaw(es []session.Entry, profile string) []session.Entry {
	var out []session.Entry
	for i, e := range es {
		if e.Kind != session.KindAssistant || len(e.Raw) == 0 || e.Profile == profile {
			continue
		}
		if out == nil {
			// a.entries is what steps and autocompact read: copy, not edit.
			out = append([]session.Entry(nil), es...)
		}
		out[i].Raw = nil
	}
	if out == nil {
		return es
	}
	return out
}
