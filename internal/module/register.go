package module

import (
	"context"
	"fmt"

	"github.com/GoldenDeals/aish-core/pkg/bus"
	"github.com/GoldenDeals/aish-core/pkg/config"
)

// Module mirrors pkg/module.Module, so its values convert here without an import cycle.
type Module interface {
	Init(b bus.Bus, cfg config.Config) error
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// Option changes how Run treats a registered module.
type Option func(*options)

type options struct {
	optIn bool // run only when the config has the module's section
}

// OptIn runs the module only when the config has its section; `name: {}` is enough.
func OptIn() Option { return func(o *options) { o.optIn = true } }

// registry and opts are filled from init() functions only, so they need no lock.
var (
	registry = map[string]Module{}
	opts     = map[string]options{}
)

func Register(name string, m Module, o ...Option) {
	switch {
	case name == "":
		panic("module: Register with an empty name")
	case m == nil:
		panic(fmt.Sprintf("module: Register %q with a nil module", name))
	}
	if _, dup := registry[name]; dup {
		panic(fmt.Sprintf("module: Register called twice for %q", name))
	}
	registry[name] = m
	var r options
	for _, f := range o {
		f(&r)
	}
	opts[name] = r
}
