package bus

import (
	"context"
	"errors"

	"github.com/GoldenDeals/aish-core/pkg/event"
	"github.com/GoldenDeals/aish-core/pkg/tx"
)

// ErrNestedTx is returned when Dispatch is called with a sync handler's ctx: it would wait for its own tx forever.
// It is detected through ctx only, so Dispatch(context.Background()) from a handler hangs.
var ErrNestedTx = errors.New("bus: dispatch from inside a tx")

// Stop is returned by a sync handler to hold the event: lower priorities and async subscribers
// don't get it, the tx goes on and Emit returns nil. Release it in a later tx with EmitIn below your priority.
var Stop = errors.New("bus: stop delivery")

// SyncHandler is part of the tx: any error except Stop, or a panic, aborts it.
// ctx ends with the tx, on commit too: use context.WithoutCancel or an async subscriber for work that outlives it.
type SyncHandler func(ctx context.Context, tx tx.Tx, ev event.Any) error

// AsyncHandler runs after commit, outside any tx; its ctx is not cancelled when Dispatch returns.
type AsyncHandler func(ctx context.Context, ev event.Any)

type Bus interface {
	// Higher priority runs first; equal priorities run in subscription order.
	// Subscriptions outlive transactions: the handler set is fixed when an event is emitted,
	// unsubscribe affects only events emitted after it returns.
	SubscribeSync(priority int, f event.Filter, h SyncHandler) (unsubscribe func())
	// No order is guaranteed. Gets only events of committed transactions whose delivery wasn't stopped.
	SubscribeAsync(f event.Filter, h AsyncHandler) (unsubscribe func())

	// Dispatch opens a new tx for a first-order event: it waits for the previous tx to finish,
	// delivers ev and returns nil on commit or the abort cause.
	// ctx bounds both the wait and the tx: when it ends, the tx aborts, but Dispatch still returns
	// only after the running handler does.
	Dispatch(ctx context.Context, ev event.Any) error
}

// Sync is SubscribeSync for event.Event[T]: events with another payload type are skipped, even if f matches.
func Sync[T any](b Bus, priority int, f event.Filter, h func(ctx context.Context, tx tx.Tx, ev event.Event[T]) error) (unsubscribe func()) {
	return b.SubscribeSync(priority, typed[T](f), func(ctx context.Context, tx tx.Tx, ev event.Any) error {
		return h(ctx, tx, ev.(event.Event[T]))
	})
}

// Async is SubscribeAsync for event.Event[T]: events with another payload type are skipped, even if f matches.
func Async[T any](b Bus, f event.Filter, h func(ctx context.Context, ev event.Event[T])) (unsubscribe func()) {
	return b.SubscribeAsync(typed[T](f), func(ctx context.Context, ev event.Any) {
		h(ctx, ev.(event.Event[T]))
	})
}

func typed[T any](f event.Filter) event.Filter {
	return func(ev event.Any) bool {
		_, ok := ev.(event.Event[T])
		return ok && f(ev)
	}
}
