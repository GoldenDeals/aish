package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/GoldenDeals/aish/internal/config"
)

// A server read by the config snapshot has the JSON, and so the cache key,
// it had when mcp read mcp.yaml itself: the caches of the tools written
// before stay the servers'. The keys were taken with LoadConfig.
func TestFromConfigCacheKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.yaml")
	yaml := `servers:
  full:
    command: npx
    args: ["-y", "srv", "--token=abc"]
    env:
      A: "1"
    url: ""
    headers:
      X: y
    env_command:
      T: pass show t
    headers_command:
      Authorization: echo b
    expose: tools
    timeout: 30
  remote:
    url: https://example.com/mcp
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	toml := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(toml, []byte("mcp_config = "+strconv.Quote(path)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AISH_CONFIG", toml)
	cfgs, err := config.NewSnapshot().Servers()
	if err != nil {
		t.Fatal(err)
	}
	servers := FromConfig(cfgs)
	for name, want := range map[string]string{
		"full":   `{"command":"npx","args":["-y","srv","--token=abc"],"env":{"A":"1"},"headers":{"X":"y"},"env_command":{"T":"pass show t"},"headers_command":{"Authorization":"echo b"},"expose":"tools","timeout":30}`,
		"remote": `{"url":"https://example.com/mcp"}`,
	} {
		if b, _ := json.Marshal(servers[name]); string(b) != want {
			t.Errorf("%s: %s, want %s", name, b, want)
		}
	}
	keys := map[string]string{}
	for _, s := range NewManager(servers, t.TempDir()).list() {
		keys[s.name] = s.key
	}
	if keys["full"] != "460be9d40be0ea3c" || keys["remote"] != "577e08845cd257d9" || len(keys) != 2 {
		t.Errorf("keys %v", keys)
	}
	if FromConfig(nil) != nil {
		t.Error("no servers made some")
	}
}
