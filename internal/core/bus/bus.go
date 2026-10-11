package bus

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/GoldenDeals/aish-core/internal/core/tx"
	"github.com/GoldenDeals/aish-core/pkg/bus"
	"github.com/GoldenDeals/aish-core/pkg/event"
)

var _ bus.Bus = (*Bus)(nil)

// txKey marks the contexts of this bus's transactions; the value is the *tx.Tx.
type txKey struct{ b *Bus }

type Bus struct {
	mu            sync.Mutex
	syncs, asyncs []*tx.Sub // copy-on-write: emits iterate published slices without the lock

	turn   chan struct{}         // one tx at a time
	cur    atomic.Pointer[tx.Tx] // tx holding turn, until its OnAbort funcs are done
	lastID uint64                // guarded by turn
}

func New() *Bus { return &Bus{turn: make(chan struct{}, 1)} }

func (b *Bus) SubscribeSync(priority int, f event.Filter, h bus.SyncHandler) (unsubscribe func()) {
	s := &tx.Sub{Prio: priority, F: f, Sync: h}
	b.mu.Lock()
	defer b.mu.Unlock()
	i := slices.IndexFunc(b.syncs, func(x *tx.Sub) bool { return x.Prio < priority })
	if i < 0 {
		i = len(b.syncs)
	}
	b.syncs = slices.Insert(slices.Clone(b.syncs), i, s)
	return func() { b.remove(&b.syncs, s) }
}

func (b *Bus) SubscribeAsync(f event.Filter, h bus.AsyncHandler) (unsubscribe func()) {
	s := &tx.Sub{F: f, Async: h}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.asyncs = append(slices.Clip(b.asyncs), s)
	return func() { b.remove(&b.asyncs, s) }
}

func (b *Bus) remove(subs *[]*tx.Sub, s *tx.Sub) {
	b.mu.Lock()
	defer b.mu.Unlock()
	*subs = slices.DeleteFunc(slices.Clone(*subs), func(x *tx.Sub) bool { return x == s })
}

func (b *Bus) snapshot() (syncs, asyncs []*tx.Sub) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.syncs, b.asyncs
}

func (b *Bus) Dispatch(ctx context.Context, ev event.Any) error {
	// Compare with the current tx, not just the marker: a WithoutCancel ctx kept from an old tx may dispatch.
	if t, _ := ctx.Value(txKey{b}).(*tx.Tx); t != nil && t == b.cur.Load() {
		return bus.ErrNestedTx
	}
	select {
	case b.turn <- struct{}{}:
	case <-ctx.Done():
		return context.Cause(ctx)
	}
	defer func() { <-b.turn }()

	b.lastID++
	t := tx.New(ctx, b.lastID, txKey{b}, b.snapshot)
	b.cur.Store(t)
	defer b.cur.Store(nil)
	return t.Run(ev)
}
