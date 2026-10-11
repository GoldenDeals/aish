package main

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

const helloSrc = `package hello

import (
	"context"
	"fmt"

	"github.com/GoldenDeals/aish-core/pkg/bus"
	"github.com/GoldenDeals/aish-core/pkg/config"
	"github.com/GoldenDeals/aish-core/pkg/event"
	"github.com/GoldenDeals/aish-core/pkg/module"
)

var greeting = "hello from the bus"

type hello struct{ b bus.Bus }

func init() { module.Register("hello", &hello{}) }

func (h *hello) Init(b bus.Bus, _ config.Config) error {
	h.b = b
	bus.Async(b, event.ByName("hello"), func(_ context.Context, ev event.Event[string]) { fmt.Println(ev.Data()) })
	return nil
}

func (h *hello) Start(ctx context.Context) error {
	return h.b.Dispatch(ctx, event.New("hello", greeting))
}

func (h *hello) Stop(context.Context) error {
	fmt.Println("bye")
	return nil
}
`

// TestBuild builds a binary with an external module from a local dir and a linker flag,
// runs it and stops it with SIGINT.
func TestBuild(t *testing.T) {
	if testing.Short() || runtime.GOOS == "windows" {
		t.Skip("builds a binary and sends it SIGINT")
	}
	mod := t.TempDir()
	for name, src := range map[string]string{"go.mod": "module example.com/hello\n\ngo 1.27\n", "hello.go": helloSrc} {
		if err := os.WriteFile(filepath.Join(mod, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	d, err := parseDep("example.com/hello=" + mod)
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "aish")
	if err := build([]dep{d}, "", bin, []string{"-ldflags=-X example.com/hello.greeting=linked"}); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer time.AfterFunc(time.Minute, func() { cmd.Process.Kill() }).Stop()
	lines := bufio.NewScanner(stdout)
	expect := func(want string) {
		t.Helper()
		for lines.Scan() {
			if lines.Text() == want {
				return
			}
		}
		t.Fatalf("binary exited before printing %q (%v)", want, lines.Err())
	}

	expect("linked")
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	expect("bye")
	if err := cmd.Wait(); err != nil {
		t.Fatalf("binary exited with %v", err)
	}
}

func TestParseDep(t *testing.T) {
	for in, want := range map[string]dep{
		"example.com/a":        {path: "example.com/a"},
		"example.com/a@v1.2.0": {path: "example.com/a", version: "v1.2.0"},
		"example.com/a=../a":   {path: "example.com/a", replace: "../a"},
		"example.com/a=/x@y/a": {path: "example.com/a", replace: "/x@y/a"},
	} {
		if got, err := parseDep(in); err != nil || got != want {
			t.Errorf("parseDep(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "@v1", "example.com/a@v1=../a"} {
		if _, err := parseDep(in); err == nil {
			t.Errorf("parseDep(%q): no error", in)
		}
	}
}
