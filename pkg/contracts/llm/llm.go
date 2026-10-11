// Package llm is the bus contract between an LLM provider and the modules around it.
//
// The provider keeps the session context. Any module adds to its system prompt with a System event:
// once (from Start) or for a user message, by holding the message above the provider's priority,
// emitting System and releasing it. A UserMessage closes the context of its tx: the provider handles it
// after everyone and answers with an Answer stream, followed by an Error if something failed.
// Dispatch a UserMessage with a ctx from WithTurn to be able to stop its answer and to know when it's over.
package llm

import "context"

const (
	System      = "llm.system"   // event.Event[string]: a system prompt fragment for the rest of the session; repeats are dropped
	UserMessage = "user.message" // event.Event[string]: the user's turn, answered by the provider
	Answer      = "llm.answer"   // event.Stream[string]: text deltas of the answer, in markdown (renderer.Markdown)
	Error       = "llm.error"    // event.Event[string]: the request failed or its answer is incomplete
)

type turnKey struct{}

type turn struct {
	context.Context
	end context.CancelFunc
}

// WithTurn marks ctx for dispatching a UserMessage: cancel stops the answer to it, even after Dispatch returns,
// and the answer ends with an Error. The cancellation of ctx itself still bounds only the tx.
// Turn(ctx).Done() closes once the turn is over: stopped, or ended by the provider.
func WithTurn(ctx context.Context) (context.Context, context.CancelFunc) {
	t, cancel := context.WithCancel(context.Background())
	return context.WithValue(ctx, turnKey{}, turn{t, cancel}), cancel
}

// Turn returns the turn set by WithTurn on ctx or its parents, or a ctx that is never cancelled.
func Turn(ctx context.Context) context.Context {
	if t, ok := ctx.Value(turnKey{}).(turn); ok {
		return t.Context
	}
	return context.Background()
}

// End is called by the provider once its Answer and Error for the turn on ctx are out.
func End(ctx context.Context) {
	if t, ok := ctx.Value(turnKey{}).(turn); ok {
		t.end()
	}
}
