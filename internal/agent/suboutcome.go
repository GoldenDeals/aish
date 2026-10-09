package agent

import (
	"errors"
	"fmt"
	"strings"

	"github.com/GoldenDeals/aish/internal/capture"
	"github.com/GoldenDeals/aish/internal/session"
)

// How a subagent ended reaches the host as the status of its block (sub.go):
// ok, error, or partial when max_steps stopped it. The host's model takes
// "ok" for work done, so what was lost or refused must not pass for it.

// statusPartial is the status of a subagent stopped at max_steps: its
// answer is what it had written by then.
const statusPartial = "partial"

// errSubAsk is what a subagent's UI answers a question with: nobody can be
// asked from it, though there is a terminal.
var errSubAsk = errors.New("a subagent cannot ask the user")

// askFailed is the reason an ask verdict is a deny when the UI could not
// ask, err telling why: no terminal, or a subagent's.
func askFailed(err error) string {
	if errors.Is(err, errSubAsk) {
		return "needs the user's confirmation, which a subagent cannot ask"
	}
	return "needs confirmation, " + err.Error()
}

// stoppedNote is the answer drive records for a request stopped at max_steps
// steps.
func stoppedNote(steps int) string {
	return fmt.Sprintf("(stopped after %d steps)", steps)
}

// subAnswer is the final answer of a subagent from its journal es, cut to
// max bytes: the text of its last turn. Stopped at steps, the last turn is
// drive's note, which would lose all it found: the answer is the last text
// it wrote before, with a line that says it is partial.
func subAnswer(es []session.Entry, steps, max int) (reply string, partial bool) {
	i := len(es) - 1
	for i >= 0 && es[i].Kind != session.KindAssistant {
		i--
	}
	if i < 0 {
		return "", false
	}
	// A turn of the model has its provider; drive's note has none.
	if e := es[i]; steps <= 0 || e.Provider != "" || e.Text != stoppedNote(steps) {
		return capture.Truncate(strings.TrimSpace(e.Text), max), false
	}
	for i--; i >= 0; i-- {
		if e := es[i]; e.Kind == session.KindAssistant && strings.TrimSpace(e.Text) != "" {
			reply = capture.Truncate(strings.TrimSpace(e.Text), max)
			break
		}
	}
	note := fmt.Sprintf("[stopped after %d steps: the answer is partial]", steps)
	return strings.TrimSpace(reply + "\n\n" + note), true
}
