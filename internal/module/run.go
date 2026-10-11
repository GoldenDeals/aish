package module

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"

	"github.com/GoldenDeals/aish-core/internal/config"
	corebus "github.com/GoldenDeals/aish-core/internal/core/bus"
	"github.com/GoldenDeals/aish-core/pkg/bus"
)

// shortcut: fixed stop timeout, move to config when it exists
const stopTimeout = 10 * time.Second

func Main() {
	slog.SetDefault(slog.New(slog.DiscardHandler)) // off until the log module sets it up: stderr would break the TUI
	path := flag.String("config", "", "YAML config file, a top-level block per module name (default "+config.Default+" if it exists)")
	flag.Parse()
	cfg, err := config.Load(*path)
	if err == nil {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		err = Run(ctx, corebus.New(), enabled(cfg), cfg)
		stop()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// enabled is the registry without the opt-in modules that have no config section.
func enabled(cfg map[string]map[string]any) map[string]Module {
	mods := maps.Clone(registry)
	for n, o := range opts {
		if _, ok := cfg[n]; o.optIn && !ok {
			delete(mods, n)
		}
	}
	return mods
}

// Run inits mods in name order with their cfg sections, starts them, waits for ctx to end
// and stops the started ones in reverse.
func Run(ctx context.Context, b bus.Bus, mods map[string]Module, cfg map[string]map[string]any) error {
	if len(mods) == 0 {
		return errors.New("module: no modules to run")
	}
	for _, n := range slices.Sorted(maps.Keys(cfg)) {
		if _, ok := mods[n]; !ok {
			return fmt.Errorf("config: section %q matches no module", n) // most likely a typo
		}
	}
	names := slices.Sorted(maps.Keys(mods))
	for _, n := range names {
		sec := config.Section(cfg[n])
		if sec == nil {
			sec = config.Section{} // modules read with defaults and never get a nil map
		}
		if err := mods[n].Init(b, sec); err != nil {
			return fmt.Errorf("module %s: init: %w", n, err)
		}
	}

	var err error
	var started []string
	for _, n := range names {
		if err = mods[n].Start(ctx); err != nil {
			err = fmt.Errorf("module %s: start: %w", n, err)
			break
		}
		started = append(started, n)
	}
	if err == nil {
		<-ctx.Done()
	}

	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stopTimeout)
	defer cancel()
	for _, n := range slices.Backward(started) {
		if serr := mods[n].Stop(sctx); serr != nil {
			err = errors.Join(err, fmt.Errorf("module %s: stop: %w", n, serr))
		}
	}
	return err
}
