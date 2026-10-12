// Package serve is the module a tool runs in: it declares the tool on Start and answers each of its
// tool.call events with a tool.result, each call in its own goroutine.
package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/GoldenDeals/aish-core/contracts/llm"
	"github.com/GoldenDeals/aish-core/contracts/tool"
	"github.com/GoldenDeals/aish-core/pkg/bus"
	"github.com/GoldenDeals/aish-core/pkg/config"
	"github.com/GoldenDeals/aish-core/pkg/event"
	"github.com/GoldenDeals/aish-core/pkg/module"
)

// limit caps an output: the dialog only grows, and one that doesn't fit the context breaks every request after it.
const limit = 30000

// Register registers the module of a tool. run gets the call's input decoded into In and a ctx that ends
// when the turn is stopped; its error makes the output an error, after the text run returned.
func Register[In any](name string, spec tool.Spec, run func(ctx context.Context, in In) (string, error)) {
	module.Register(name, &mod[In]{name: name, spec: spec, run: run})
}

type mod[In any] struct {
	name   string
	spec   tool.Spec
	run    func(context.Context, In) (string, error)
	b      bus.Bus
	ctx    context.Context // ends on Stop, stopping calls in flight
	cancel context.CancelFunc
}

func (m *mod[In]) Init(b bus.Bus, _ config.Config) error {
	m.b = b
	m.ctx, m.cancel = context.WithCancel(context.Background())
	bus.Async(b, func(ev event.Any) bool {
		u, ok := ev.(event.Event[tool.Use])
		return ok && u.Name() == tool.Call && u.Data().Name == m.spec.Name
	}, m.call)
	return nil
}

func (m *mod[In]) Start(ctx context.Context) error {
	return m.b.Dispatch(ctx, event.New(tool.Declare, m.spec))
}

func (m *mod[In]) Stop(context.Context) error {
	m.cancel()
	return nil
}

func (m *mod[In]) call(actx context.Context, ev event.Event[tool.Use]) {
	u := ev.Data()
	slog.Info("tool call", "module", m.name, "id", u.ID)
	slog.Debug("tool call", "module", m.name, "id", u.ID, "input", string(u.Input))
	ctx, cancel := context.WithCancel(m.ctx)
	defer cancel()
	defer context.AfterFunc(llm.Turn(actx), cancel)() // the user stopped the turn

	var in In
	text, err := "", json.Unmarshal(u.Input, &in)
	if err == nil {
		text, err = m.run(ctx, in)
	}
	if err != nil {
		slog.Warn("tool error", "module", m.name, "id", u.ID, "err", err)
		if text != "" {
			text += "\n"
		}
		text += err.Error()
	}
	text = Cut(text)
	slog.Debug("tool result", "module", m.name, "id", u.ID, "text", text)
	_ = m.b.Dispatch(actx, event.New(tool.Result, tool.Output{ID: u.ID, Text: text, Error: err != nil})) // a failed tx has nowhere to report to
}

// Cut caps a tool's output at limit bytes, keeping both ends: a listing starts with what matters, a build ends with it.
func Cut(text string) string {
	if n := len(text); n > limit {
		return text[:limit/2] + fmt.Sprintf("\n… %d bytes cut …\n", n-limit) + text[n-limit/2:]
	}
	return text
}
