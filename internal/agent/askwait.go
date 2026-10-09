package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// A question waits for the user, but not for ever: the user may have gone,
// and the request would stand till they are back. A question of the policy
// left unanswered is declined, the form of ask_user is closed (ask_timeout),
// and the request goes on: the model learns that nobody answered.

// The policy's Yes/No questions wait askFirst; each one more left without
// an answer in a row waits half as long as the one before, but no less than
// askLeast: a request left alone runs through its questions in some twenty
// minutes, not in ten each. An answer, Yes or No, or a new request gives
// askFirst again; a key that is no answer does not.
// Variables: the tests shorten them.
var (
	askFirst = 10 * time.Minute
	askLeast = 15 * time.Second
)

// askWaits is how long the next question of the policy waits. It lives
// with the agent through the commands of a request, Resume keeping it;
// Start resets it.
type askWaits struct {
	next time.Duration // 0 is askFirst
}

func (w *askWaits) wait() time.Duration {
	if w.next == 0 {
		return askFirst
	}
	return w.next
}

// missed is the question that waited wait() and got no answer.
func (w *askWaits) missed() { w.next = max(w.wait()/2, askLeast) }

// reset is an answer, or a new request.
func (w *askWaits) reset() { w.next = 0 }

// waitAnswer is ctx ending after d, a question's time to be answered. Its
// cause is what the UI shows on the question it closes.
func waitAnswer(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeoutCause(ctx, d, errors.New("no answer in "+span(d)))
}

// timedOut tells whether the error of a question is its time running out,
// not ctx of the request ending.
func timedOut(ctx context.Context, err error) bool {
	return errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil
}

// noAnswer is the result of an ask_user call nobody answered in d: not an
// instruction, the model decides what follows.
func noAnswer(d time.Duration) string {
	return fmt.Sprintf("no answer: the user did not answer in %s and may be away; decide yourself whether to go on without the answers", span(d))
}

// span is d as a person writes it: 10m, 2m30s, 37.5s, 1h.
func span(d time.Duration) string {
	s := d.String()
	if t, ok := strings.CutSuffix(s, "m0s"); ok {
		s = t + "m"
	}
	if t, ok := strings.CutSuffix(s, "h0m"); ok {
		s = t + "h"
	}
	return s
}
