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
)

// aish model run by the assistant is refused before the models are asked
// for or what looks like a switch is printed; listing them stays. The
// proxy lists them: it has the key.
func TestModelAsking(t *testing.T) {
	for _, c := range []struct {
		name   string
		asking bool
		args   []string
		listed bool // rpc models came
		model  bool // rpc model came
	}{
		{"switch while asking", true, []string{"other"}, false, false},
		{"switch", false, []string{"other"}, true, true},
		{"list while asking", true, nil, true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			var listed, model atomic.Bool
			l, err := net.Listen("unix", filepath.Join(t.TempDir(), "sock"))
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			cfg := config.Default()
			cfg.Provider = "anthropic"
			go rpc.Serve(l, func(_ context.Context, method string, _ json.RawMessage) (any, error) {
				switch method {
				case rpc.MethodInfo:
					return rpc.Info{Model: "m", Asking: c.asking}, nil
				case rpc.MethodConfig:
					return rpc.Config{Config: cfg}, nil
				case rpc.MethodModels:
					listed.Store(true)
					return nil, errors.New("404 Not Found")
				case rpc.MethodModel:
					model.Store(true)
					return nil, errors.New("refused")
				}
				t.Errorf("unexpected rpc %s", method)
				return nil, errors.New("unexpected")
			})
			t.Setenv("AISH_SOCK", l.Addr().String())

			if code := modelCmd(config.Default(), c.args); code == 0 {
				t.Error("exit code 0")
			}
			if listed.Load() != c.listed {
				t.Errorf("rpc models: %v, want %v", listed.Load(), c.listed)
			}
			if model.Load() != c.model {
				t.Errorf("rpc model: %v, want %v", model.Load(), c.model)
			}
		})
	}
}
