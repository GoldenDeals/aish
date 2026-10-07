package agent

import "strings"

// policyPrompt is the section of the system prompt that tells the model
// the hints of the policy in force, so that it keeps to the rules rather
// than finds them out by a refusal and a turn spent on it. It depends on
// the policy alone, which stays until `aish apply-config`: the system
// prompt, which the provider caches first, stays the same from turn to
// turn. "" when the policy has no hints.
func (a *Agent) policyPrompt() string {
	hints := a.Policy.Hints()
	if len(hints) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("# Policy\n" +
		"The user's policy limits what you may do. Follow these rules from the start instead of finding them out by being refused:")
	for _, h := range hints {
		b.WriteString("\n- ")
		b.WriteString(h)
	}
	return b.String()
}
