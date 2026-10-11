package tx

import (
	"context"
	"errors"

	"github.com/GoldenDeals/aish-core/pkg/event"
)

// ErrClosed is returned by Emit once the tx has started aborting or is finalized.
var ErrClosed = errors.New("tx: closed")

// Tx holds one first-order event and every internal event it causes.
// It commits when the sync handlers of all its events have returned and every event.Stream
// emitted in it is finalized; it aborts on the first handler error or panic, or when the Dispatch context ends.
// Tx is not safe for concurrent use: call it only from the handler's goroutine.
type Tx interface {
	ID() uint64

	// Emit runs matching sync handlers in the caller's goroutine, by priority, and returns
	// when they are done; async subscribers get ev after commit.
	// A handler error aborts the whole tx and is returned here as well.
	// Pass the handler's ctx: any other ctx is replaced by the tx's one.
	Emit(ctx context.Context, ev event.Any) error
	// EmitIn is Emit limited to sync handlers with lo <= priority <= hi; async subscribers are not filtered.
	// Equal: (p, p); from p: (p, math.MaxInt); up to p: (math.MinInt, p). lo > hi panics.
	EmitIn(ctx context.Context, ev event.Any, lo, hi int) error

	// OnAbort runs after abort in reverse registration order, before the next tx starts.
	// Undo in-memory state changes here.
	OnAbort(func())
}
