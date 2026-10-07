package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/rpc"
)

// aish trust run while the assistant asks is refused, should the policies
// have been got around: trusted.json is not even created. Listing the
// files trusted stays.
func TestTrustAsking(t *testing.T) {
	for _, c := range []struct {
		name   string
		asking bool
		args   []string
		code   int
	}{
		{"trust while asking", true, nil, 1},
		{"revoke while asking", true, []string{"--revoke"}, 1},
		{"list while asking", true, []string{"--list"}, 0},
		{"trust", false, nil, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("HOME", filepath.Join(root, "home"))
			t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
			repo := filepath.Join(root, "repo")
			if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(repo, config.ProjectFile), []byte("hooks_dir = \".aish/hooks\"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			t.Chdir(repo)

			l, err := net.Listen("unix", filepath.Join(t.TempDir(), "sock"))
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			go rpc.Serve(l, func(_ context.Context, method string, _ json.RawMessage) (any, error) {
				if method == rpc.MethodInfo {
					return rpc.Info{Model: "m", Asking: c.asking}, nil
				}
				t.Errorf("unexpected rpc %s", method)
				return nil, errors.New("unexpected")
			})
			t.Setenv("AISH_SOCK", l.Addr().String())

			if code := trustCmd(config.Default(), c.args); code != c.code {
				t.Errorf("exit code %d, want %d", code, c.code)
			}
			_, err = os.Stat(config.TrustFile())
			if created := err == nil; created != (c.code == 0 && c.args == nil) {
				t.Errorf("trusted.json created: %v", created)
			}
		})
	}
}
