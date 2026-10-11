package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"go.yaml.in/yaml/v3"
)

// Section is a module's top-level block of the config file; it implements pkg/config.Config.
type Section map[string]any

func (s Section) Map() map[string]any { return s }

// Default is read when no path is given, relative to the working directory; unlike a given path it may be missing.
const Default = ".agents/aish.yaml"

// Load reads a YAML file whose top-level keys are module names; an empty path means Default.
//
//	hello:
//	  greeting: hi
//
// Sections are plain maps: yaml.v3 decodes nested blocks into the named map type it is given.
func Load(path string) (map[string]map[string]any, error) {
	if path == "" {
		if _, err := os.Stat(Default); errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		path = Default
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
