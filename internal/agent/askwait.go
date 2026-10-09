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

// AnswerTimer is a UI that keeps the time of its questions itself, so as
// to stop it while the user cannot see the question: the proxy stops it
// while the Ctrl+O viewer covers the question, the user reading there
// being no user gone. Its questions, Ask and Form, get the time on ctx
// (AnswerTime) instead of a deadline, and end when it is out as at one:
// with context.DeadlineExceeded, the cause on the screen.
type AnswerTimer interface {
	TimesAnswers()
}

// waitAnswer is ctx of a question that has d to be answered: past d, it
// ends with the cause the UI shows on the question it closes. An
// AnswerTimer gets d on ctx instead, and the cancel stops nothing.
func (a *Agent) waitAnswer(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	ui := a.UI
	if a.work != nil {
		ui = a.work.ui // the agent's, under the line of hidden calls
	}
	if _, ok := ui.(AnswerTimer); ok {
		return WithAnswerTime(ctx, d), func() {}
	}
	return context.WithTimeoutCause(ctx, d, unanswered(d))
}

type answerKey struct{}

type answerTime struct {
	d   time.Duration
	why error
}

// WithAnswerTime is ctx of a question that has d to be answered, for an
// AnswerTimer to keep.
func WithAnswerTime(ctx context.Context, d time.Duration) context.Context {
	return context.WithValue(ctx, answerKey{}, answerTime{d, unanswered(d)})
}

// AnswerTime is the time the question of ctx has to be answered, given to
// an AnswerTimer, and the cause it ends with unanswered. Without it, the
// question waits as long as ctx.
func AnswerTime(ctx context.Context) (d time.Duration, why error, ok bool) {
	t, ok := ctx.Value(answerKey{}).(answerTime)
	return t.d, t.why, ok
}

// unanswered is the cause a question that had d ends with.
func unanswered(d time.Duration) error { return errors.New("no answer in " + span(d)) }

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
