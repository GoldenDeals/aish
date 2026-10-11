package tx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"runtime/debug"
	"slices"

	"github.com/GoldenDeals/aish-core/pkg/bus"
	"github.com/GoldenDeals/aish-core/pkg/event"
	"github.com/GoldenDeals/aish-core/pkg/tx"
)

var _ tx.Tx = (*Tx)(nil)

// Sub is a subscription: Sync is set for sync ones, Async for async ones.
type Sub struct {
	Prio  int
	F     event.Filter
	Sync  bus.SyncHandler
	Async bus.AsyncHandler
}

// Subs returns the current subscribers, syncs sorted by priority desc; callers must not modify the slices.
type Subs func() (syncs, asyncs []*Sub)

type rec struct {
	ev      event.Any
	asyncs  []*Sub // async subscribers at emit time
	stopped bool
}

// Tx keeps its state in ctx: the cancel cause is the abort cause, or tx.ErrClosed once finished.
type Tx struct {
	id     uint64
	key    any // marks contexts of this bus's transactions, for the nested Dispatch check
	subs   Subs
	ctx    context.Context
	cancel context.CancelCauseFunc
	actx   context.Context // for async handlers: the Dispatch ctx without its cancellation
	events []*rec          // in emit order
	undo   []func()
}

// New starts a tx whose ctx maps key to it.
func New(ctx context.Context, id uint64, key any, subs Subs) *Tx {
	t := &Tx{id: id, key: key, subs: subs, actx: context.WithoutCancel(ctx)}
	t.ctx, t.cancel = context.WithCancelCause(context.WithValue(ctx, key, t))
	return t
}

// Run delivers the first-order event, waits for the streams emitted in the tx, then either
// rolls back and returns the abort cause or commits and starts async delivery.
func (t *Tx) Run(ev event.Any) error {
	defer t.cancel(tx.ErrClosed)
	_ = t.emit(t.ctx, ev, math.MinInt, math.MaxInt) // the outcome is in t.ctx
	for _, r := range t.events {
		if w, ok := r.ev.(interface{ Wait(context.Context) error }); ok && t.ctx.Err() == nil {
			if err := w.Wait(t.ctx); err != nil {
				t.cancel(err)
			}
		}
	}
	if err := context.Cause(t.ctx); err != nil {
		err = t.rollback(err)
		slog.Warn("tx rolled back", "tx", t.id, "event", ev.Name(), "err", err)
		return err
	}

	t.cancel(tx.ErrClosed)
	slog.Debug("tx committed", "tx", t.id, "events", len(t.events))
	for _, r := range t.events {
		if r.stopped {
			continue
		}
		for _, s := range r.asyncs {
			// The filter runs here too, so its panic can't escape a committed Dispatch.
			go func() {
				if s.F(r.ev) {
					s.Async(t.actx, r.ev)
				}
			}()
		}
	}
	return nil
}

func (t *Tx) ID() uint64 { return t.id }

func (t *Tx) OnAbort(f func()) { t.undo = append(t.undo, f) }

func (t *Tx) Emit(ctx context.Context, ev event.Any) error {
	return t.EmitIn(ctx, ev, math.MinInt, math.MaxInt)
}

func (t *Tx) EmitIn(ctx context.Context, ev event.Any, lo, hi int) error {
	if lo > hi {
		panic(fmt.Sprintf("bus: EmitIn: lo %d > hi %d", lo, hi))
	}
	if ctx == nil || ctx.Value(t.key) != t {
		ctx = t.ctx // a foreign ctx would outlive the tx and hide it from the nested Dispatch check
	}
	return t.emit(ctx, ev, lo, hi)
}

func (t *Tx) emit(ctx context.Context, ev event.Any, lo, hi int) error {
	if err := t.closed(); err != nil {
		return err
	}
	slog.Debug("emit", "tx", t.id, "event", ev.Name())
	syncs, asyncs := t.subs()
	r := &rec{ev: ev, asyncs: asyncs}
	t.events = append(t.events, r)
	for _, s := range syncs {
		if s.Prio > hi {
			continue
		}
		if s.Prio < lo {
			break
		}
		err := protect(func() error {
			if !s.F(ev) {
				return nil
			}
			return s.Sync(ctx, t, ev)
		})
		if err != nil && !errors.Is(err, bus.Stop) {
			t.cancel(err)
			return err
		}
		// An abort while the handler ran (deadline, swallowed error) beats its Stop.
		if cerr := t.closed(); cerr != nil {
			return cerr
		}
		if err != nil {
			r.stopped = true
			return nil
		}
	}
	return nil
}

func (t *Tx) closed() error {
	c := context.Cause(t.ctx)
	if c == nil || errors.Is(c, tx.ErrClosed) {
		return c
	}
	return fmt.Errorf("%w: %w", tx.ErrClosed, c)
}

func (t *Tx) rollback(err error) error {
	for _, f := range slices.Backward(t.undo) {
		if perr := protect(func() error { f(); return nil }); perr != nil {
			err = errors.Join(err, perr)
		}
	}
	return err
}

func protect(f func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("bus: panic: %v\n%s", r, debug.Stack())
		}
	}()
	return f()
}
