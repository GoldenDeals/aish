package module

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/GoldenDeals/aish-core/pkg/bus"
	"github.com/GoldenDeals/aish-core/pkg/config"
)

type fake struct {
	name              string
	log               *[]string
	initErr, startErr error
	cfg               config.Config
}

func (f *fake) Init(_ bus.Bus, cfg config.Config) error {
	*f.log = append(*f.log, "init "+f.name)
	f.cfg = cfg
	return f.initErr
}

func (f *fake) Start(context.Context) error {
	*f.log = append(*f.log, "start "+f.name)
	return f.startErr
}

func (f *fake) Stop(context.Context) error {
	*f.log = append(*f.log, "stop "+f.name)
	return nil
}

func fakes(log *[]string, names ...string) map[string]Module {
	mods := map[string]Module{}
	for _, n := range names {
		mods[n] = &fake{name: n, log: log}
	}
	return mods
}

func TestRunOrder(t *testing.T) {
	var log []string
	mods := fakes(&log, "b", "c", "a")
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // shut down right after start

	if err := Run(ctx, nil, mods, nil); err != nil {
		t.Fatal(err)
	}
	want := []string{"init a", "init b", "init c", "start a", "start b", "start c", "stop c", "stop b", "stop a"}
	if !slices.Equal(log, want) {
		t.Fatalf("got %v, want %v", log, want)
	}
	for n, m := range mods {
		if cfg := m.(*fake).cfg; cfg == nil || cfg.Map() == nil {
			t.Errorf("%s got no config", n)
		}
	}
}

func TestRunStartError(t *testing.T) {
	var log []string
	mods := fakes(&log, "a", "b", "c")
	boom := errors.New("boom")
	mods["b"].(*fake).startErr = boom

	err := Run(context.Background(), nil, mods, nil)
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "module b: start") {
		t.Fatalf("Run: %v", err)
	}
	if want := []string{"init a", "init b", "init c", "start a", "start b", "stop a"}; !slices.Equal(log, want) {
		t.Fatalf("got %v, want %v", log, want)
	}
}

func TestRunInitError(t *testing.T) {
	var log []string
	mods := fakes(&log, "a", "b", "c")
	mods["b"].(*fake).initErr = errors.New("boom")

	if err := Run(context.Background(), nil, mods, nil); err == nil || !strings.Contains(err.Error(), "module b: init") {
		t.Fatalf("Run: %v", err)
	}
	if want := []string{"init a", "init b"}; !slices.Equal(log, want) {
		t.Fatalf("got %v, want %v", log, want)
	}
}

func TestRunConfig(t *testing.T) {
	var log []string
	mods := fakes(&log, "a")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cfg := map[string]map[string]any{"a": {"greeting": "hi"}}
	if err := Run(ctx, nil, mods, cfg); err != nil {
		t.Fatal(err)
	}
	if g := mods["a"].(*fake).cfg.Map()["greeting"]; g != "hi" {
		t.Errorf("greeting = %v", g)
	}

	cfg["typo"] = nil
	if err := Run(ctx, nil, mods, cfg); err == nil || !strings.Contains(err.Error(), `"typo"`) {
		t.Fatalf("Run with an unknown section: %v", err)
	}
}

func TestRunEmpty(t *testing.T) {
	if err := Run(context.Background(), nil, nil, nil); err == nil {
		t.Fatal("Run without modules returned nil")
	}
}

func TestRegister(t *testing.T) {
	Register("test.ok", &fake{})
	t.Cleanup(func() { delete(registry, "test.ok") })
	for name, f := range map[string]func(){
		"duplicate": func() { Register("test.ok", &fake{}) },
		"empty":     func() { Register("", &fake{}) },
		"nil":       func() { Register("test.nil", nil) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: no panic", name)
				}
			}()
			f()
		}()
	}
}
