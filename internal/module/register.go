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

// registry is filled from init() functions only, so it needs no lock.
var registry = map[string]Module{}

func Register(name string, m Module) {
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
}
