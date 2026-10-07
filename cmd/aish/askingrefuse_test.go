package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/session"
)

// aish clear, aish new and aish resume run by the assistant are refused
// before their rpc: aish resume would open the picker or look the session
// up first.
func TestAskingRefuse(t *testing.T) {
	dir := t.TempDir()
	s, err := session.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	s.Unlock()
	cfg := config.Default()
	cfg.SessionsDir = dir

	for _, c := range []struct {
		name   string
		method string
		run    func() int
	}{
		{"clear", rpc.MethodClear, func() int { return clearCmd(nil) }},
		{"clear save", rpc.MethodClear, func() int { return clearCmd([]string{"save", "x"}) }},
		{"new", rpc.MethodClear, func() int { return newCmd(nil) }},
		{"resume", rpc.MethodResume, func() int { return resumeCmd(nil, cfg, []string{s.ID}) }},
	} {
		for _, asking := range []bool{true, false} {
			name := c.name
			if asking {
				name += " while asking"
			}
			t.Run(name, func(t *testing.T) {
				var called atomic.Bool
				l, err := net.Listen("unix", filepath.Join(t.TempDir(), "sock"))
				if err != nil {
					t.Fatal(err)
				}
				defer l.Close()
				go rpc.Serve(l, func(_ context.Context, method string, _ json.RawMessage) (any, error) {
					switch method {
					case rpc.MethodInfo:
						return rpc.Info{SessionID: "other", Asking: asking}, nil
					case c.method:
						called.Store(true)
						return nil, errors.New("refused")
					}
					t.Errorf("unexpected rpc %s", method)
					return nil, errors.New("unexpected")
				})
				t.Setenv("AISH_SOCK", l.Addr().String())

				if code := c.run(); code == 0 {
					t.Error("exit code 0")
				}
				if called.Load() == asking {
					t.Errorf("rpc %s: %v, want %v", c.method, called.Load(), !asking)
				}
			})
		}
	}
}
