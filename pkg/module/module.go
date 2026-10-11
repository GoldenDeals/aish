package module

import (
	"context"

	impl "github.com/GoldenDeals/aish-core/internal/module"
	"github.com/GoldenDeals/aish-core/pkg/bus"
	"github.com/GoldenDeals/aish-core/pkg/config"
)

// Module is a part of the system that talks to the others only through the bus.
type Module interface {
	// Init subscribes to the bus. Don't Dispatch here: other modules may not be subscribed yet.
	Init(b bus.Bus, cfg config.Config) error
	// Start begins work once every module is initialized. Return quickly; ctx ends on shutdown.
	Start(ctx context.Context) error
	// Stop releases resources; modules stop in reverse order, ctx bounds the time.
	Stop(ctx context.Context) error
}

// Register adds m under a unique name, which is also its config section key. Call it from init().
func Register(name string, m Module) { impl.Register(name, m) }

// Main runs the registered modules until SIGINT or SIGTERM; the generated main.go calls it.
func Main() { impl.Main() }
