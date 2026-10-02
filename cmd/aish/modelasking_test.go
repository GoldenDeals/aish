package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/inebotov/aish/internal/config"
	"github.com/inebotov/aish/internal/rpc"
)

// aish model run by the assistant is refused before it asks the API for
// the models or prints what looks like a switch; listing them stays.
func TestModelAsking(t *testing.T) {
	for _, c := range []struct {
		name   string
		asking bool
		args   []string
		api    bool // the API was asked for the models
		model  bool // rpc model came
	}{
		{"switch while asking", true, []string{"other"}, false, false},
		{"switch", false, []string{"other"}, true, true},
		{"list while asking", true, nil, true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			var api, model atomic.Bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				api.Store(true)
				http.NotFound(w, r)
			}))
			defer srv.Close()

			l, err := net.Listen("unix", filepath.Join(t.TempDir(), "sock"))
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			go rpc.Serve(l, func(_ context.Context, method string, _ json.RawMessage) (any, error) {
				switch method {
				case rpc.MethodInfo:
					return rpc.Info{Model: "m", Asking: c.asking}, nil
				case rpc.MethodModel:
					model.Store(true)
					return nil, errors.New("refused")
				}
				t.Errorf("unexpected rpc %s", method)
				return nil, errors.New("unexpected")
			})
			t.Setenv("AISH_SOCK", l.Addr().String())

			cfg := config.Default()
			cfg.Provider, cfg.APIKey, cfg.BaseURL = "anthropic", "k", srv.URL
			if code := modelCmd(cfg, c.args); code == 0 {
				t.Error("exit code 0")
			}
			if api.Load() != c.api {
				t.Errorf("API asked: %v, want %v", api.Load(), c.api)
			}
			if model.Load() != c.model {
				t.Errorf("rpc model: %v, want %v", model.Load(), c.model)
			}
		})
	}
}
