package serve

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/GoldenDeals/aish-core/contracts/llm"
	"github.com/GoldenDeals/aish-core/contracts/tool"
	corebus "github.com/GoldenDeals/aish-core/internal/core/bus"
	"github.com/GoldenDeals/aish-core/pkg/bus"
	"github.com/GoldenDeals/aish-core/pkg/event"
	"github.com/GoldenDeals/aish-core/pkg/tx"
)

func TestCall(t *testing.T) {
	b := corebus.New()
	m := &mod[struct{ N int }]{name: "tools.t", spec: tool.Spec{Name: "t"}, run: func(ctx context.Context, in struct{ N int }) (string, error) {
		switch in.N {
		case 1:
			return "partial", errors.New("boom")
		case 2:
			return strings.Repeat("a", limit) + strings.Repeat("b", 10), nil
		case 3:
			<-ctx.Done()
			return "", ctx.Err()
		}
		return "ok", nil
	}}
	results, specs := make(chan tool.Output, 10), make(chan tool.Spec, 1)
	bus.Sync(b, 0, event.ByName(tool.Result), func(_ context.Context, _ tx.Tx, ev event.Event[tool.Output]) error {
		results <- ev.Data()
		return nil
	})
	bus.Sync(b, 0, event.ByName(tool.Declare), func(_ context.Context, _ tx.Tx, ev event.Event[tool.Spec]) error {
		specs <- ev.Data()
		return nil
	})
	if err := m.Init(b, nil); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(context.Background()); err != nil || (<-specs).Name != "t" {
		t.Fatal("no declaration", err)
	}
	defer m.Stop(context.Background())

	call := func(ctx context.Context, id, name, input string) tool.Output {
		t.Helper()
		if err := b.Dispatch(ctx, event.New(tool.Call, tool.Use{ID: id, Name: name, Input: []byte(input)})); err != nil {
			t.Fatal(err)
		}
		select {
		case r := <-results:
			return r
		case <-time.After(5 * time.Second):
			t.Fatal("no result")
			panic("unreachable")
		}
	}
	if r := call(context.Background(), "0", "t", `{"n":0}`); r != (tool.Output{ID: "0", Text: "ok"}) {
		t.Errorf("ok: %+v", r)
	}
	if r := call(context.Background(), "1", "t", `{"n":1}`); r != (tool.Output{ID: "1", Text: "partial\nboom", Error: true}) {
		t.Errorf("error: %+v", r)
	}
	if r := call(context.Background(), "2", "t", `{"n":2}`); len(r.Text) > limit+50 || !strings.HasPrefix(r.Text, "a") ||
		!strings.HasSuffix(r.Text, "b") || !strings.Contains(r.Text, "… 10 bytes cut …") {
		t.Errorf("long output: %d bytes", len(r.Text))
	}
	if r := call(context.Background(), "3", "t", `[`); !r.Error {
		t.Errorf("bad input: %+v", r)
	}
	if err := b.Dispatch(context.Background(), event.New(tool.Call, tool.Use{ID: "4", Name: "other"})); err != nil {
		t.Fatal(err)
	}

	ctx, stop := llm.WithTurn(context.Background())
	time.AfterFunc(50*time.Millisecond, stop)
	if r := call(ctx, "5", "t", `{"n":3}`); r != (tool.Output{ID: "5", Text: "context canceled", Error: true}) {
		t.Errorf("stopped turn: %+v", r)
	}
	select {
	case r := <-results:
		t.Errorf("a result for another tool: %+v", r)
	default:
	}
}
