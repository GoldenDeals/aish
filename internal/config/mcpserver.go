package config

import (
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
	"go.yaml.in/yaml/v3"
)

// MCPServer is one server of the MCP config, mcp.yaml: a command speaking
// MCP on stdio, or the URL of a Streamable HTTP endpoint. The servers are
// the snapshot's (Snapshot.Servers), not a Config's: they are aish's, not
// a profile's or a project's, and their env and headers hold secrets that
// the config the commands in the shell get must not carry. Package mcp
// runs them as its Server, of the same fields.
type MCPServer struct {
	Command string            `yaml:"command" toml:"command"`
	Args    []string          `yaml:"args" toml:"args"`
	Env     map[string]string `yaml:"env" toml:"env"`
	URL     string            `yaml:"url" toml:"url"`
	Headers map[string]string `yaml:"headers" toml:"headers"`
	// EnvCommand and HeadersCommand give a variable or a header the output
	// of a command instead of a literal: a token kept in pass, say. A key
	// is in one of the two maps, not in both (checkCommands).
	EnvCommand     map[string]string `yaml:"env_command" toml:"env_command"`
	HeadersCommand map[string]string `yaml:"headers_command" toml:"headers_command"`
	// Expose is "deferred" (the default), for tools the model sees by name
	// and loads with tool_search, or "tools", whose schemas it is given with
	// every request.
	Expose string `yaml:"expose" toml:"expose"`
	// Timeout of a tool call, in seconds.
	Timeout int `yaml:"timeout" toml:"timeout"`
}

// mcpFile is the MCP config that config.toml, read as data, names: its
// mcp_config, which only the top level has. A file that does not decode
// names the default; Load fails on it anyway.
func mcpFile(data []byte, readErr error) string {
	f := struct {
		MCPConfig string `toml:"mcp_config"`
	}{Default().MCPConfig}
	if readErr == nil {
		if _, err := toml.Decode(string(data), &f); err != nil {
			f.MCPConfig = Default().MCPConfig
		}
	}
	return expand(f.MCPConfig)
}

// parseServers reads the MCP config at path. A key it does not know is
// passed over, as it always was.
func parseServers(path string, data []byte) (map[string]MCPServer, error) {
	var f struct {
		Servers map[string]MCPServer `yaml:"servers"`
	}
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := checkServers(path, f.Servers); err != nil {
		return nil, err
	}
	return f.Servers, nil
}

// checkServers refuses a server of the MCP config at path that is neither
// a command nor a URL, or both, one with an expose aish does not know, and
// one checkCommands refuses: each would fail, or lose a value, only when
// it is started, if at all.
func checkServers(path string, servers map[string]MCPServer) error {
	for _, name := range slices.Sorted(maps.Keys(servers)) {
		s := servers[name]
		if (s.Command == "") == (s.URL == "") {
			return fmt.Errorf("%s: server %s needs either command or url", path, name)
		}
		if s.Expose != "" && s.Expose != "deferred" && s.Expose != "tools" {
			return fmt.Errorf("%s: server %s: expose must be deferred or tools", path, name)
		}
		if err := s.checkCommands(); err != nil {
			return fmt.Errorf("%s: server %s: %w", path, name, err)
		}
	}
	return nil
}

// checkCommands refuses a key given both a literal and a command, of
// which one would be lost without a word, and an empty command. A header
// is one key whatever its case, as HTTP has it.
func (s MCPServer) checkCommands() error {
	for _, m := range []struct {
		name     string
		lit, cmd map[string]string
		key      func(string) string
	}{
		{"env", s.Env, s.EnvCommand, func(k string) string { return k }},
		{"headers", s.Headers, s.HeadersCommand, http.CanonicalHeaderKey},
	} {
		lit := map[string]bool{}
		for k := range m.lit {
			lit[m.key(k)] = true
		}
		for _, k := range slices.Sorted(maps.Keys(m.cmd)) {
			if lit[m.key(k)] {
				return fmt.Errorf("%s is in both %s and %s_command", k, m.name, m.name)
			}
			if strings.TrimSpace(m.cmd[k]) == "" {
				return fmt.Errorf("%s_command %s: empty command", m.name, k)
			}
		}
	}
	return nil
}
