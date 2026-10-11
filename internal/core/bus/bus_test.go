package bus_test

import (
	"context"
	"errors"
	"math"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	core "github.com/GoldenDeals/aish-core/internal/core/bus"
	"github.com/GoldenDeals/aish-core/pkg/bus"
	"github.com/GoldenDeals/aish-core/pkg/event"
	"github.com/GoldenDeals/aish-core/pkg/tx"
)

func named(name string) event.Any { return event.New(name, struct{}{}) }

func all(event.Any) bool { return true }

// names is a log that handlers in other goroutines can write to.
type names struct {
	mu sync.Mutex
	s  []string
}

func (n *names) add(s string) { n.mu.Lock(); n.s = append(n.s, s); n.mu.Unlock() }

func (n *names) list() []string { n.mu.Lock(); defer n.mu.Unlock(); return slices.Clone(n.s) }

func (n *names) sorted() []string { return slices.Sorted(slices.Values(n.list())) }

func (n *names) asyncHandler(_ context.Context, ev event.Any) { n.add(ev.Name()) }

func TestOrder(t *testing.T) {
	b := core.New()
	ctx := context.Background()
	var got []string
	logAs := func(name string) bus.SyncHandler {
		return func(context.Context, tx.Tx, event.Any) error { got = append(got, name); return nil }
	}
	b.SubscribeSync(0, event.ByName("x"), logAs("0"))
	b.SubscribeSync(100, event.ByName("x"), logAs("100a"))
	b.SubscribeSync(1000, event.ByName("x"), logAs("1000"))
	b.SubscribeSync(100, event.ByName("x"), logAs("100b"))

	if err := b.Dispatch(ctx, named("x")); err != nil {
		t.Fatal(err)
	}
	if want := []string{"1000", "100a", "100b", "0"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}

	cases := []struct {
		lo, hi int
		want   []string
	}{
		{100, 100, []string{"100a", "100b"}},
		{100, math.MaxInt, []string{"1000", "100a", "100b"}},
		{math.MinInt, 100, []string{"100a", "100b", "0"}},
	}
	b.SubscribeSync(5000, event.ByName("ranges"), func(ctx context.Context, tr tx.Tx, _ event.Any) error {
		for _, c := range cases {
			got = nil
			if err := tr.EmitIn(ctx, named("x"), c.lo, c.hi); err != nil {
				return err
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("EmitIn(%d, %d): got %v, want %v", c.lo, c.hi, got, c.want)
			}
		}
		return nil
	})
	if err := b.Dispatch(ctx, named("ranges")); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshots(t *testing.T) {
	b := core.New()
	ctx := context.Background()
	var got []string
	var unsub func()
	subscribed := false
	b.SubscribeSync(10, event.ByName("x"), func(context.Context, tx.Tx, event.Any) error {
		got = append(got, "first")
		unsub() // second time it's a no-op
		if !subscribed {
			subscribed = true
			b.SubscribeSync(0, event.ByName("x"), func(context.Context, tx.Tx, event.Any) error {
				got = append(got, "late")
				return nil
			})
		}
		return nil
	})
	unsub = b.SubscribeSync(5, event.ByName("x"), func(context.Context, tx.Tx, event.Any) error {
		got = append(got, "second")
		return nil
	})

	for _, want := range [][]string{{"first", "second"}, {"first", "late"}} {
		got = nil
		if err := b.Dispatch(ctx, named("x")); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// The permission prompt: hold tool.call in tx1, release it in tx2 when the answer arrives.
func TestHoldRelease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := core.New()
		ctx := context.Background()
		const prio = 1000
		var held event.Any
		var ran []string
		var async names
		b.SubscribeAsync(all, async.asyncHandler)
		b.SubscribeSync(prio, event.ByName("tool.call"), func(ctx context.Context, tr tx.Tx, ev event.Any) error {
			held = ev
			if err := tr.Emit(ctx, named("permission.request")); err != nil {
				return err
			}
			return bus.Stop
		})
		b.SubscribeSync(prio, event.ByName("permission.response"), func(ctx context.Context, tr tx.Tx, _ event.Any) error {
			return tr.EmitIn(ctx, held, math.MinInt, prio-1)
		})
		b.SubscribeSync(0, event.ByName("tool.call"), func(context.Context, tx.Tx, event.Any) error {
			ran = append(ran, "tool")
			return nil
		})

		if err := b.Dispatch(ctx, named("tool.call")); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if len(ran) != 0 {
			t.Fatal("tool ran before permission")
		}
		if got, want := async.sorted(), []string{"permission.request"}; !slices.Equal(got, want) {
			t.Fatalf("async after tx1: got %v, want %v", got, want)
		}

		if err := b.Dispatch(ctx, named("permission.response")); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if !slices.Equal(ran, []string{"tool"}) {
			t.Fatalf("tool ran %v after release", ran)
		}
		if got, want := async.sorted(), []string{"permission.request", "permission.response", "tool.call"}; !slices.Equal(got, want) {
			t.Fatalf("async after tx2: got %v, want %v", got, want)
		}
	})
}

func TestAbort(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := core.New()
		boom := errors.New("boom")
		var undo, async names
		var nestedCtx context.Context
		b.SubscribeAsync(all, async.asyncHandler)
		b.SubscribeSync(0, event.ByName("x"), func(ctx context.Context, tr tx.Tx, _ event.Any) error {
			tr.OnAbort(func() { undo.add("x") })
			_ = tr.Emit(context.Background(), named("y")) // swallowed: the tx must abort anyway
			if err := tr.Emit(ctx, named("z")); !errors.Is(err, tx.ErrClosed) || !errors.Is(err, boom) {
				t.Errorf("Emit after abort: %v", err)
			}
			return nil
		})
		b.SubscribeSync(0, event.ByName("y"), func(ctx context.Context, tr tx.Tx, _ event.Any) error {
			nestedCtx = ctx
			tr.OnAbort(func() { undo.add("y1") })
			tr.OnAbort(func() { panic("undo panic") })
			tr.OnAbort(func() { undo.add("y2") })
			return boom
		})

		err := b.Dispatch(context.Background(), named("x"))
		if !errors.Is(err, boom) || !strings.Contains(err.Error(), "undo panic") {
			t.Fatalf("Dispatch: %v", err)
		}
		if got, want := undo.list(), []string{"y2", "y1", "x"}; !slices.Equal(got, want) {
			t.Fatalf("OnAbort ran %v, want %v", got, want)
		}
		if nestedCtx.Err() == nil {
			t.Fatal("handler ctx not cancelled on abort")
		}
		synctest.Wait()
		if got := async.list(); len(got) != 0 {
			t.Fatalf("async got %v from an aborted tx", got)
		}
	})
}

func TestPanic(t *testing.T) {
	b := core.New()
	b.SubscribeSync(0, event.ByName("handler"), func(context.Context, tx.Tx, event.Any) error { panic("handler boom") })
	b.SubscribeSync(0, func(ev event.Any) bool {
		if ev.Name() == "filter" {
			panic("filter boom")
		}
		return false
	}, func(context.Context, tx.Tx, event.Any) error { return nil })
	b.SubscribeSync(0, event.ByName("range"), func(ctx context.Context, tr tx.Tx, _ event.Any) error {
		return tr.EmitIn(ctx, named("q"), 1, 0)
	})

	for name, want := range map[string]string{"handler": "handler boom", "filter": "filter boom", "range": "lo 1 > hi 0"} {
		if err := b.Dispatch(context.Background(), named(name)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want an error with %q", name, err, want)
		}
	}
}

func TestFinish(t *testing.T) {
	b := core.New()
	var stale tx.Tx
	var hctx context.Context
	actx := make(chan context.Context, 1)
	b.SubscribeSync(0, event.ByName("x"), func(ctx context.Context, tr tx.Tx, _ event.Any) error {
		stale, hctx = tr, ctx
		return nil
	})
	b.SubscribeAsync(event.ByName("x"), func(ctx context.Context, _ event.Any) { actx <- ctx })

	if err := b.Dispatch(context.Background(), named("x")); err != nil {
		t.Fatal(err)
	}
	if err := stale.Emit(context.Background(), named("y")); !errors.Is(err, tx.ErrClosed) {
		t.Fatalf("Emit on a committed tx: %v", err)
	}
	if hctx.Err() == nil {
		t.Fatal("handler ctx alive after commit")
	}
	if (<-actx).Err() != nil {
		t.Fatal("async ctx cancelled after commit")
	}
}

func TestSerial(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := core.New()
		ctx := context.Background()
		var log names
		release := make(chan struct{})
		b.SubscribeSync(0, event.ByName("1"), func(_ context.Context, tr tx.Tx, _ event.Any) error {
			log.add("1")
			tr.OnAbort(func() { log.add("undo1") })
			<-release
			return errors.New("boom")
		})
		b.SubscribeSync(0, event.ByName("2"), func(context.Context, tx.Tx, event.Any) error {
			log.add("2")
			return nil
		})

		go b.Dispatch(ctx, named("1"))
		synctest.Wait()
		done2 := make(chan error, 1)
		go func() { done2 <- b.Dispatch(ctx, named("2")) }()
		cctx, cancel := context.WithCancelCause(ctx)
		done3 := make(chan error, 1)
		go func() { done3 <- b.Dispatch(cctx, named("2")) }()
		synctest.Wait()
		if got := log.list(); !slices.Equal(got, []string{"1"}) {
			t.Fatalf("second tx started early: %v", got)
		}

		stop := errors.New("stop waiting")
		cancel(stop)
		if err := <-done3; !errors.Is(err, stop) {
			t.Fatalf("waiting Dispatch: %v", err)
		}
		close(release)
		if err := <-done2; err != nil {
			t.Fatal(err)
		}
		if got, want := log.list(), []string{"1", "undo1", "2"}; !slices.Equal(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})
}

func TestNested(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := core.New()
		ctx := context.Background()
		var detached context.Context
		var got names
		b.SubscribeSync(0, event.ByName("x"), func(ctx context.Context, _ tx.Tx, _ event.Any) error {
			detached = context.WithoutCancel(ctx)
			for _, c := range []context.Context{ctx, detached} {
				if err := b.Dispatch(c, named("y")); !errors.Is(err, bus.ErrNestedTx) {
					t.Errorf("nested Dispatch: %v", err)
				}
			}
			return nil
		})
		b.SubscribeSync(0, event.ByName("y"), func(context.Context, tx.Tx, event.Any) error {
			got.add("y")
			return nil
		})
		b.SubscribeAsync(event.ByName("a"), func(ctx context.Context, _ event.Any) {
			if err := b.Dispatch(ctx, named("y")); err != nil {
				t.Error(err)
			}
		})

		if err := b.Dispatch(ctx, named("x")); err != nil {
			t.Fatal(err)
		}
		if err := b.Dispatch(detached, named("y")); err != nil {
			t.Fatalf("Dispatch with a ctx kept from a finished tx: %v", err)
		}
		if err := b.Dispatch(ctx, named("a")); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if got, want := got.list(), []string{"y", "y"}; !slices.Equal(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})
}

func TestDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := core.New()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		undone, called := false, false
		b.SubscribeSync(10, event.ByName("x"), func(_ context.Context, tr tx.Tx, _ event.Any) error {
			tr.OnAbort(func() { undone = true })
			time.Sleep(2 * time.Second) // ignores ctx
			return nil
		})
		b.SubscribeSync(5, event.ByName("x"), func(context.Context, tx.Tx, event.Any) error {
			called = true
			return nil
		})

		if err := b.Dispatch(ctx, named("x")); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Dispatch: %v", err)
		}
		if called {
			t.Fatal("handler called after the deadline")
		}
		if !undone {
			t.Fatal("OnAbort didn't run")
		}
	})
}

func TestStream(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := core.New()
		var mu sync.Mutex
		var final bool
		var data []string
		b.SubscribeSync(0, event.ByName("go"), func(ctx context.Context, tr tx.Tx, _ event.Any) error {
			src := make(chan string)
			go func() { // lives on the handler ctx, so commit must wait for it
				defer close(src)
				for _, s := range []string{"a", "b"} {
					select {
					case src <- s:
					case <-ctx.Done():
						return
					}
				}
			}()
			return tr.Emit(ctx, event.NewStream("llm", src))
		})
		b.SubscribeAsync(event.ByName("llm"), func(_ context.Context, ev event.Any) {
			mu.Lock()
			defer mu.Unlock()
			final, data = event.Finalized(ev), ev.(event.Stream[string]).Data()
		})

		if err := b.Dispatch(context.Background(), named("go")); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		mu.Lock()
		if !final || !slices.Equal(data, []string{"a", "b"}) {
			t.Fatalf("async saw finalized=%v data=%v", final, data)
		}
		mu.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		hang := make(chan string)
		defer close(hang) // let the stream goroutine exit before the bubble ends
		b.SubscribeSync(0, event.ByName("hang"), func(ctx context.Context, tr tx.Tx, _ event.Any) error {
			return tr.Emit(ctx, event.NewStream("never", hang))
		})
		if err := b.Dispatch(ctx, named("hang")); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("unfinished stream: %v", err)
		}
	})
}
