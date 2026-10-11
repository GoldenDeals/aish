package bus

import (
	"context"
	"testing"

	"github.com/GoldenDeals/aish-core/pkg/event"
	"github.com/GoldenDeals/aish-core/pkg/tx"
)

// fakeBus records the last sync subscription.
type fakeBus struct {
	f event.Filter
	h SyncHandler
}

func (b *fakeBus) SubscribeSync(_ int, f event.Filter, h SyncHandler) func() {
	b.f, b.h = f, h
	return func() {}
}
func (b *fakeBus) SubscribeAsync(event.Filter, AsyncHandler) func() { return func() {} }
func (b *fakeBus) Dispatch(context.Context, event.Any) error        { return nil }

func TestSyncTyped(t *testing.T) {
	b := &fakeBus{}
	var got string
	Sync(b, 0, event.ByName("tool.call"), func(_ context.Context, _ tx.Tx, ev event.Event[string]) error {
		got = ev.Data()
		return nil
	})

	if b.f(event.New("tool.call", 42)) {
		t.Fatal("matched an event with another payload type")
	}
	if b.f(event.New("other", "x")) {
		t.Fatal("matched an event with another name")
	}
	ev := event.New("tool.call", "ls")
	if !b.f(ev) {
		t.Fatal("did not match")
	}
	if err := b.h(context.Background(), nil, ev); err != nil || got != "ls" {
		t.Fatalf("handler got %q, err %v", got, err)
	}
}
