package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/inebotov/aish/internal/llm"
)

// retryWaits are the pauses before each retry of a turn; a variable for the
// tests.
var retryWaits = []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}

// complete is Provider.Complete with retries of what the API failed on its
// side. A retry starts the reply over: what the failed attempt streamed
// stays on the screen, under a note that it was cut. restart shows the note
// on a line of its own and readies onText for a new reply.
func (a *Agent) complete(ctx context.Context, req llm.Request, onText func(string), restart func(note string)) (*llm.Response, error) {
	for i := 0; ; i++ {
		resp, err := a.Provider.Complete(ctx, req, onText)
		if err == nil || i == len(retryWaits) || ctx.Err() != nil || !llm.Retryable(err) {
			return resp, err
		}
		wait := retryWaits[i]
		restart(fmt.Sprintf("[aish: %s; retrying in %s (%d/%d)]", firstLine(err.Error()), wait, i+1, len(retryWaits)))
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-t.C:
		}
	}
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}
