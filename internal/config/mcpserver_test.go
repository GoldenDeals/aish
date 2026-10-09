package config

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// mcpOf writes config.toml naming the MCP config at a path of its own, and
// gives the path.
func mcpOf(t *testing.T, yaml string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "servers.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	toml := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(toml, []byte("mcp_config = "+strconv.Quote(path)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AISH_CONFIG", toml)
	return path
}

// names are the servers of s.
func names(t *testing.T, s *Snapshot) []string {
	t.Helper()
	servers, err := s.Servers()
	if err != nil {
		t.Fatal(err)
	}
	return slices.Sorted(maps.Keys(servers))
}

// The MCP config is read with config.toml and kept as it was: an edit is
// the next snapshot's, told of by Stale and Changed as config.toml's is.
// A broken one is an error of Servers alone: the requests go on.
func TestSnapshotServers(t *testing.T) {
	path := mcpOf(t, "servers:\n  a:\n    command: x\n")
	s := NewSnapshot()
	if got := names(t, s); !slices.Equal(got, []string{"a"}) || len(s.Stale()) > 0 || len(s.Changed("/")) > 0 {
		t.Fatalf("servers %q, stale %q, changed %q", got, s.Stale(), s.Changed("/"))
	}

	if err := os.WriteFile(path, []byte("servers:\n  a:\n    command: x\n  b:\n    url: https://h\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := names(t, s); !slices.Equal(got, []string{"a"}) {
		t.Errorf("the snapshot after an edit: %q", got)
	}
	changed := s.Changed("/")
	if len(changed) != 1 || !slices.Equal(s.Stale(), []string{path}) {
		t.Errorf("changed %q, stale %q", changed, s.Stale())
	}
	if err := os.WriteFile(path, []byte("servers:\n  b:\n    url: https://h\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if again := s.Changed("/"); len(again) != 1 || again[0] == changed[0] {
		t.Errorf("another edit, keys %q and %q", changed, again)
	}
	if got := names(t, NewSnapshot()); !slices.Equal(got, []string{"b"}) {
		t.Errorf("the next snapshot: %q", got)
	}

	if err := os.WriteFile(path, []byte("servers: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	broken := NewSnapshot()
	if _, err := broken.Servers(); err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("a broken MCP config: %v", err)
	}
	if _, err := broken.LoadEnv(func(string) string { return "" }); err != nil {
		t.Errorf("a broken MCP config fails LoadEnv: %v", err)
	}
	if _, err := broken.LoadProfile(""); err != nil {
		t.Errorf("a broken MCP config fails LoadProfile: %v", err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if servers, err := NewSnapshot().Servers(); err != nil || servers != nil {
		t.Errorf("no file: %v %v", servers, err)
	}
	if !slices.Equal(s.Stale(), []string{path}) || !slices.Equal(broken.Stale(), []string{path}) {
		t.Errorf("gone: stale %q and %q", s.Stale(), broken.Stale())
	}
}

// Without mcp_config it is mcp.yaml next to config.toml's default place;
// mcp_config may start with ~/.
func TestSnapshotServersPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "conf"))
	toml := filepath.Join(home, "config.toml")
	t.Setenv("AISH_CONFIG", toml)
	for _, tc := range []struct{ toml, mcp string }{
		{"", filepath.Join(home, "conf", "aish", "mcp.yaml")},
		{"mcp_config = \"~/m.yaml\"\n", filepath.Join(home, "m.yaml")},
	} {
		if err := os.WriteFile(toml, []byte(tc.toml), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(tc.mcp), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(tc.mcp, []byte("servers:\n  s:\n    command: x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := names(t, NewSnapshot()); !slices.Equal(got, []string{"s"}) {
			t.Errorf("%q: %q", tc.toml, got)
		}
		if err := os.Remove(tc.mcp); err != nil {
			t.Fatal(err)
		}
	}
}

// A FIFO in place of the MCP config keeps neither aish's start nor a
// request waiting for a writer.
func TestSnapshotServersFIFO(t *testing.T) {
	path := mcpOf(t, "")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	mkfifo(t, path)
	s := quick(t, path, NewSnapshot)
	if _, err := s.Servers(); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("a FIFO: %v", err)
	}
	if changed := quick(t, path, func() []string { return s.Changed("/") }); len(changed) > 0 {
		t.Errorf("changed %q", changed)
	}
	if stale := quick(t, path, s.Stale); len(stale) > 0 {
		t.Errorf("stale %q", stale)
	}
}

// A server is a command or a URL, exposed as aish knows, and has a value
// of env or headers given once; the error names the file and the server.
// A key the config does not know is passed over, as it always was.
func TestCheckServers(t *testing.T) {
	path := mcpOf(t, `servers:
  tracker:
    command: npx
    env:
      GRAFANA_URL: https://grafana.example
    env_command:
      TRACKER_TOKEN: pass show tracker/token
    expose: tools
    unknown_key: x
  remote:
    url: https://example.com/mcp
    headers:
      Accept: application/json
    headers_command:
      Authorization: echo "Bearer $(pass show example.com/token)"
    expose: deferred
`)
	servers, err := NewSnapshot().Servers()
	if err != nil {
		t.Fatal(err)
	}
	if got := servers["tracker"].EnvCommand["TRACKER_TOKEN"]; got != "pass show tracker/token" {
		t.Errorf("env_command %q", got)
	}
	if got := servers["remote"].HeadersCommand["Authorization"]; got != `echo "Bearer $(pass show example.com/token)"` {
		t.Errorf("headers_command %q", got)
	}

	for _, tc := range []struct{ yaml, want string }{
		{"servers:\n  s:\n    args: [x]\n", "server s needs either command or url"},
		{"servers:\n  s:\n    command: x\n    url: https://h\n", "server s needs either command or url"},
		{"servers:\n  s:\n    command: x\n    expose: commands\n", "server s: expose must be deferred or tools"},
		{"servers:\n  s:\n    command: x\n    expose: all\n", "server s: expose must be deferred or tools"},
		{"servers:\n  s:\n    command: x\n    env: {T: a}\n    env_command: {T: printf b}\n",
			"server s: T is in both env and env_command"},
		{"servers:\n  s:\n    url: https://h\n    headers: {Authorization: a}\n    headers_command: {authorization: printf b}\n",
			"server s: authorization is in both headers and headers_command"},
		{"servers:\n  s:\n    command: x\n    env_command: {T: \"\"}\n", "server s: env_command T: empty command"},
		{"servers:\n  s:\n    url: https://h\n    headers_command: {X-Key: \"  \"}\n", "server s: headers_command X-Key: empty command"},
	} {
		if err := os.WriteFile(path, []byte(tc.yaml), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewSnapshot().Servers(); err == nil || !strings.Contains(err.Error(), tc.want) || !strings.HasPrefix(err.Error(), path+": ") {
			t.Errorf("%q: %v, want %q and the file", tc.yaml, err, tc.want)
		}
	}
	// The same name in env and headers is no clash: they are not one thing.
	if err := os.WriteFile(path, []byte("servers:\n  s:\n    command: x\n    env: {Authorization: a}\n    headers_command: {Authorization: printf b}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSnapshot().Servers(); err != nil {
		t.Errorf("env and headers_command of one name: %v", err)
	}
}
