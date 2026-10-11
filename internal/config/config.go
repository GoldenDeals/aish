package config

import (
	"fmt"
	"os"

	"go.yaml.in/yaml/v3"
)

// Section is a module's top-level block of the config file; it implements pkg/config.Config.
type Section map[string]any

func (s Section) Map() map[string]any { return s }

// Load reads a YAML file whose top-level keys are module names; an empty path means no file.
//
//	hello:
//	  greeting: hi
//
// Sections are plain maps: yaml.v3 decodes nested blocks into the named map type it is given.
func Load(path string) (map[string]map[string]any, error) {
	if path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	var secs map[string]map[string]any
	if err := yaml.Unmarshal(b, &secs); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	return secs, nil
}
