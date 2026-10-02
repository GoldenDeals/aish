package policy

import (
	"context"
	"strings"
)

// Checker is one source of verdicts; Engine asks every one and keeps the
// strictest answer.
type Checker interface {
	Check(ctx context.Context, in Input) (Decision, error)
}

// combine folds the decisions of one tool call into the worst of them: deny
// over ask over allow, with the reasons joined and deduplicated. A checker
// that has nothing against the call must still return allow, never an empty
// decision.
func combine(ds []Decision) Decision {
	out := Decision{Action: Allow}
	var reasons []string
	seen := map[string]bool{}
	for _, d := range ds {
		if rank(d.Action) > rank(out.Action) {
			out.Action = d.Action
			reasons, seen = nil, map[string]bool{}
		}
		if d.Action != out.Action {
			continue
		}
		for _, r := range strings.Split(d.Reason, "; ") {
			if r != "" && !seen[r] {
				seen[r] = true
				reasons = append(reasons, r)
			}
		}
	}
	out.Reason = strings.Join(reasons, "; ")
	return out
}

func rank(action string) int {
	switch action {
	case Deny:
		return 2
	case Ask:
		return 1
	}
	return 0
}
