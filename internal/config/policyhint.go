package config

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// checkHints refuses a hint of no rule: a typo in its pattern would leave
// the rule without its text quietly, and so would an empty text.
func (p Policy) checkHints() error {
	for _, pat := range slices.Sorted(maps.Keys(p.Hints)) {
		if !slices.Contains(p.Deny, pat) && !slices.Contains(p.Ask, pat) {
			return fmt.Errorf("policy.hints[%q]: no such pattern in deny or ask", pat)
		}
		if strings.TrimSpace(p.Hints[pat]) == "" {
			return fmt.Errorf("policy.hints[%q]: empty hint", pat)
		}
	}
	return p.checkWriteHint()
}

func (p Policy) checkWriteHint() error {
	if p.WriteOutsideHomeHint == "" {
		return nil
	}
	if strings.TrimSpace(p.WriteOutsideHomeHint) == "" {
		return fmt.Errorf("policy.write_outside_home_hint: empty hint")
	}
	if p.WriteOutsideHome == "" || p.WriteOutsideHome == "allow" {
		return fmt.Errorf("policy.write_outside_home_hint: no write_outside_home = \"ask\" or \"deny\" to explain")
	}
	return nil
}

// layPolicy is the [policy] of a project, pr, laid over the global one,
// g: the lists added, the stricter write_outside_home kept. The hints go
// with the rules: of a pattern both name, the two texts, the global one
// first; of write_outside_home, that of the file whose value is kept, or
// both when the values are alike, or the global one when the project's
// has none. The project's hints may name the global patterns, which the
// check of the whole config sees to; its write_outside_home_hint must
// explain its own write_outside_home, the one it is read with.
func layPolicy(g, pr Policy) (Policy, error) {
	if err := pr.checkWriteHint(); err != nil {
		return g, err
	}
	for _, pat := range slices.Sorted(maps.Keys(pr.Hints)) {
		if strings.TrimSpace(pr.Hints[pat]) == "" {
			return g, fmt.Errorf("policy.hints[%q]: empty hint", pat)
		}
	}
	out := Policy{
		Deny:                 slices.Concat(g.Deny, pr.Deny),
		Ask:                  slices.Concat(g.Ask, pr.Ask),
		WriteOutsideHome:     stricter(g.WriteOutsideHome, pr.WriteOutsideHome),
		Hints:                maps.Clone(g.Hints),
		WriteOutsideHomeHint: g.WriteOutsideHomeHint,
	}
	for pat, text := range pr.Hints {
		if out.Hints == nil {
			out.Hints = map[string]string{}
		}
		out.Hints[pat] = joinHints(out.Hints[pat], text)
	}
	if text := pr.WriteOutsideHomeHint; text != "" {
		switch {
		case stricter(g.WriteOutsideHome, pr.WriteOutsideHome) != g.WriteOutsideHome:
			out.WriteOutsideHomeHint = text
		case stricter(pr.WriteOutsideHome, g.WriteOutsideHome) == pr.WriteOutsideHome:
			// Neither is stricter: both explain the rule in force.
			out.WriteOutsideHomeHint = joinHints(g.WriteOutsideHomeHint, text)
		}
	}
	return out, nil
}

// joinHints is the hint of a rule two files explain, a's text first; a
// text said twice, once.
func joinHints(a, b string) string {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == "" || a == b {
		return b
	}
	return a + " " + b
}
